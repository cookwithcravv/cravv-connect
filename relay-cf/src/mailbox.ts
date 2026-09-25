import { DurableObject } from "cloudflare:workers";
import {
  authMessage,
  b64decode,
  b64encode,
  decodeIK,
  mailboxIdOf,
  randomBytes,
  randomNameplate,
  randomToken,
  sha256Hex,
  verifyEd25519,
} from "./crypto";
import type { Env } from "./env";
import { rateLimitsEnabled, readLimits, type Limits } from "./limits";
import {
  CLOSE_POLICY,
  Code,
  failSocket,
  MAILBOX_ID_RE,
  parseFrame,
  resFrame,
  ROUTE_IK_HEADER,
  ROUTE_MAILBOX_HEADER,
  Status,
  str,
  VERSION,
  type Frame,
  type SendStatus,
} from "./protocol";
import { REGISTRY_NAME } from "./registry";

type Stage = "hello" | "auth" | "register" | "ready";

// Per-socket state. Stored with serializeAttachment so it survives hibernation.
interface Attachment {
  stage: Stage;
  ik: string; // canonical unpadded base64 of the routing IK
  origin: string;
  nonce: string;
  mailboxId: string;
}

type Op = (ws: WebSocket, a: Attachment, f: Frame, rid: string) => Promise<void>;

const ROOM_CREATE_ATTEMPTS = 16;
const MAX_ID_CHARS = 128;

// Mailbox is one SQLite-backed Durable Object per mailbox id. It owns the queue, the allow-list
// (keyed by sender mailbox id), and the seq counter, and holds at most one live WebSocket
// (hibernation API).
export class Mailbox extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  private readonly limits: Limits;
  private readonly ops: Record<string, Op>;
  private chain: Promise<void> = Promise.resolve();
  private tokens: number;
  private refilledAt: number;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.limits = readLimits(env);
    this.tokens = this.limits.requestBurst;
    this.refilledAt = Date.now();
    this.sql.exec(`CREATE TABLE IF NOT EXISTS queue (
      seq INTEGER PRIMARY KEY,
      from_ik TEXT NOT NULL,
      id TEXT NOT NULL,
      frame TEXT NOT NULL,
      size INTEGER NOT NULL,
      created_at INTEGER NOT NULL
    )`);
    this.sql.exec("CREATE TABLE IF NOT EXISTS allow (mailbox_id TEXT PRIMARY KEY)");
    this.sql.exec("CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)");
    this.ops = {
      allow: (ws, _a, f, rid) => this.opAllow(ws, f, rid, true),
      deny: (ws, _a, f, rid) => this.opAllow(ws, f, rid, false),
      invite_request: (ws, a, _f, rid) => this.opInvite(ws, a, rid),
      room_create: (ws, _a, _f, rid) => this.opRoomCreate(ws, rid),
      send: (ws, a, f, rid) => this.opSend(ws, a, f, rid),
      register: async (ws, _a, _f, rid) => ws.send(resFrame(rid, { status: Status.OK })),
    };
  }

  // ---------- WebSocket entry (called by the Worker with the upgrade request) ----------

  async fetch(request: Request): Promise<Response> {
    const ik = request.headers.get(ROUTE_IK_HEADER);
    const mailboxId = request.headers.get(ROUTE_MAILBOX_HEADER);
    if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket" || !ik || !mailboxId) {
      return new Response("expected websocket upgrade", { status: 426 });
    }
    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    this.ctx.acceptWebSocket(server);
    const origin = this.env.PUBLIC_ORIGIN || new URL(request.url).origin;
    const a: Attachment = { stage: "hello", ik, origin, nonce: "", mailboxId };
    server.serializeAttachment(a);
    return new Response(null, { status: 101, webSocket: client });
  }

  // Messages are handled strictly one after another (relay-v1 3.1): a request sent after an
  // ack observes it, and sends to one recipient keep their order. The chain lives in memory;
  // the object cannot hibernate while a handler is still pending.
  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const run = this.chain.then(() => this.handle(ws, message));
    this.chain = run.catch(() => undefined);
    return run;
  }

  async webSocketClose(ws: WebSocket, code: number, reason: string): Promise<void> {
    try {
      ws.close(code === 1005 ? 1000 : code, reason);
    } catch {
      // already closed
    }
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    try {
      ws.close(1011, "error");
    } catch {
      // already closed
    }
  }

  private async handle(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    if (typeof message !== "string") {
      failSocket(ws, Code.BAD_REQUEST, "binary messages are not supported");
      return;
    }
    const f = parseFrame(message);
    if (!f) {
      failSocket(ws, Code.BAD_REQUEST, "malformed message");
      return;
    }
    const a = ws.deserializeAttachment() as Attachment;
    try {
      switch (a.stage) {
        case "hello":
          return this.onHello(ws, a, f);
        case "auth":
          return await this.onAuth(ws, a, f);
        case "register":
          return await this.onRegister(ws, a, f);
        case "ready":
          return await this.onReady(ws, a, f);
      }
    } catch (err) {
      failSocket(ws, Code.INTERNAL, err instanceof Error ? err.message : "internal error");
    }
  }

  // ---------- handshake ----------

  private onHello(ws: WebSocket, a: Attachment, f: Frame): void {
    if (f.t !== "hello" || !Array.isArray(f.versions)) {
      failSocket(ws, Code.BAD_REQUEST, "expected hello");
      return;
    }
    if (!f.versions.includes(VERSION)) {
      failSocket(ws, Code.UNSUPPORTED_VERSION, "this relay speaks relay-v1 only");
      return;
    }
    a.nonce = b64encode(randomBytes(32));
    a.stage = "auth";
    ws.serializeAttachment(a);
    ws.send(JSON.stringify({ t: "welcome", version: VERSION }));
    ws.send(JSON.stringify({ t: "challenge", nonce: a.nonce }));
  }

  private async onAuth(ws: WebSocket, a: Attachment, f: Frame): Promise<void> {
    if (f.t !== "auth") {
      failSocket(ws, Code.BAD_REQUEST, "expected auth");
      return;
    }
    const ik = decodeIK(f.ik);
    if (!ik || b64encode(ik) !== a.ik) {
      failSocket(ws, Code.AUTH_FAILED, "ik does not match the connect ik parameter");
      return;
    }
    let sig: Uint8Array;
    try {
      sig = b64decode(str(f.sig) ?? "");
    } catch {
      failSocket(ws, Code.AUTH_FAILED, "malformed signature");
      return;
    }
    if (!(await verifyEd25519(ik, authMessage(a.origin, a.nonce), sig))) {
      failSocket(ws, Code.AUTH_FAILED, "bad signature");
      return;
    }
    const mailboxId = await mailboxIdOf(ik);
    if (mailboxId !== a.mailboxId) {
      failSocket(ws, Code.AUTH_FAILED, "mailbox mismatch");
      return;
    }
    let registered = this.isRegistered();
    if (!registered) {
      registered = await this.registry().isMember(mailboxId);
      if (registered) this.setMeta("registered", "1");
    }
    ws.send(JSON.stringify({ t: "auth_ok", registered, mailbox_id: mailboxId }));
    if (registered) {
      this.activate(ws, a);
    } else {
      a.stage = "register";
      ws.serializeAttachment(a);
    }
  }

  private async onRegister(ws: WebSocket, a: Attachment, f: Frame): Promise<void> {
    if (f.t !== "register") {
      failSocket(ws, Code.NOT_REGISTERED, "mailbox is not registered; send register first");
      return;
    }
    const rid = str(f.rid);
    if (rid === undefined) {
      failSocket(ws, Code.BAD_REQUEST, "register needs a rid");
      return;
    }
    const ok = await this.registry().register(a.mailboxId, str(f.admin_token), str(f.invite));
    if (!ok) {
      // relay-v1 3.4 step 5: the refusal is final, so answer and then close.
      ws.send(resFrame(rid, { status: Status.ERROR, code: Code.FORBIDDEN }));
      try {
        ws.close(CLOSE_POLICY, Code.FORBIDDEN);
      } catch {
        // already closed
      }
      return;
    }
    this.setMeta("registered", "1");
    ws.send(resFrame(rid, { status: Status.OK }));
    this.activate(ws, a);
  }

  // Marks ws as the single live connection, closes older ones, and pushes the backlog.
  private activate(ws: WebSocket, a: Attachment): void {
    for (const other of this.ctx.getWebSockets()) {
      if (other === ws) continue;
      const oa = other.deserializeAttachment() as Attachment | null;
      if (oa && oa.stage === "ready") failSocket(other, Code.GONE, "replaced by a newer connection");
    }
    a.stage = "ready";
    ws.serializeAttachment(a);
    this.dropExpired(Date.now());
    const rows = this.sql.exec<{ seq: number; from_ik: string; id: string; frame: string }>(
      "SELECT seq, from_ik, id, frame FROM queue ORDER BY seq",
    );
    for (const r of rows) ws.send(deliverFrame(r.seq, r.from_ik, r.id, r.frame));
  }

  // ---------- operations after authentication ----------

  private async onReady(ws: WebSocket, a: Attachment, f: Frame): Promise<void> {
    if (f.t === "ack") {
      this.opAck(ws, f);
      return;
    }
    const rid = str(f.rid);
    if (rid === undefined) {
      failSocket(ws, Code.BAD_REQUEST, `request ${f.t} without rid`);
      return;
    }
    if (!this.takeToken()) {
      ws.send(resFrame(rid, { status: Status.RATE_LIMITED }));
      return;
    }
    const op = this.ops[f.t];
    if (!op) {
      ws.send(resFrame(rid, { status: Status.ERROR, code: Code.BAD_REQUEST }));
      return;
    }
    await op(ws, a, f, rid);
  }

  private async opAllow(ws: WebSocket, f: Frame, rid: string, allow: boolean): Promise<void> {
    const ik = decodeIK(f.ik);
    if (!ik) {
      ws.send(resFrame(rid, { status: Status.ERROR, code: Code.BAD_REQUEST }));
      return;
    }
    const sender = await mailboxIdOf(ik);
    if (allow) this.sql.exec("INSERT OR IGNORE INTO allow (mailbox_id) VALUES (?)", sender);
    else this.sql.exec("DELETE FROM allow WHERE mailbox_id = ?", sender);
    ws.send(resFrame(rid, { status: Status.OK }));
  }

  private async opInvite(ws: WebSocket, a: Attachment, rid: string): Promise<void> {
    const invite = await this.registry().createInvite(a.mailboxId);
    if (invite === null) {
      ws.send(resFrame(rid, { status: Status.ERROR, code: Code.FORBIDDEN }));
      return;
    }
    ws.send(resFrame(rid, { status: Status.OK, invite }));
  }

  private async opRoomCreate(ws: WebSocket, rid: string): Promise<void> {
    for (let i = 0; i < ROOM_CREATE_ATTEMPTS; i++) {
      const nameplate = randomNameplate();
      const token = randomToken();
      const created = await this.env.ROOM.getByName(nameplate).init(await sha256Hex(token));
      if (created) {
        ws.send(resFrame(rid, { status: Status.OK, nameplate, creator_token: token }));
        return;
      }
    }
    ws.send(resFrame(rid, { status: Status.ERROR, code: Code.INTERNAL }));
  }

  // Check order follows relay-v1 3.6 (rate limit was already applied in onReady).
  private async opSend(ws: WebSocket, a: Attachment, f: Frame, rid: string): Promise<void> {
    const to = str(f.to) ?? "";
    const id = str(f.id) ?? "";
    let frame: Uint8Array | null = null;
    try {
      const raw = str(f.frame);
      if (raw !== undefined) frame = b64decode(raw);
    } catch {
      frame = null;
    }
    if (to === "" || id === "" || id.length > MAX_ID_CHARS || frame === null) {
      ws.send(resFrame(rid, { status: Status.ERROR, code: Code.BAD_REQUEST }));
      return;
    }
    let status: SendStatus;
    if (frame.length > this.limits.maxFrameBytes) {
      status = Status.TOO_LARGE;
    } else if (!MAILBOX_ID_RE.test(to)) {
      status = Status.UNKNOWN_MAILBOX;
    } else if (to === a.mailboxId) {
      status = await this.enqueue(a.ik, a.mailboxId, id, b64encode(frame), frame.length);
    } else {
      status = await this.env.MAILBOX.getByName(to).enqueue(a.ik, a.mailboxId, id, b64encode(frame), frame.length);
    }
    ws.send(resFrame(rid, { status }));
  }

  private opAck(ws: WebSocket, f: Frame): void {
    const seq = f.seq;
    if (typeof seq !== "number" || !Number.isSafeInteger(seq) || seq < 0) {
      failSocket(ws, Code.BAD_REQUEST, "ack needs a non-negative integer seq");
      return;
    }
    this.sql.exec("DELETE FROM queue WHERE seq <= ?", seq);
  }

  // ---------- RPC: called by the sender's Mailbox DO ----------

  async enqueue(fromIk: string, fromMailbox: string, id: string, frame: string, size: number): Promise<SendStatus> {
    if (!this.isRegistered()) return Status.UNKNOWN_MAILBOX;
    if (this.sql.exec("SELECT 1 FROM allow WHERE mailbox_id = ?", fromMailbox).toArray().length === 0) {
      return Status.NOT_ALLOWED;
    }
    const now = Date.now();
    this.dropExpired(now);
    const stats = this.sql
      .exec<{ n: number; bytes: number }>("SELECT COUNT(*) AS n, COALESCE(SUM(size), 0) AS bytes FROM queue")
      .one();
    if (stats.n + 1 > this.limits.queueMaxFrames || stats.bytes + size > this.limits.queueMaxBytes) {
      return Status.QUEUE_FULL;
    }
    const seq = this.nextSeq();
    this.sql.exec(
      "INSERT INTO queue (seq, from_ik, id, frame, size, created_at) VALUES (?, ?, ?, ?, ?, ?)",
      seq,
      fromIk,
      id,
      frame,
      size,
      now,
    );
    const live = this.liveSocket();
    if (live) {
      try {
        live.send(deliverFrame(seq, fromIk, id, frame));
      } catch {
        // stays queued; redelivered on reconnect
      }
    }
    await this.ensureAlarm(now);
    return Status.QUEUED;
  }

  // ---------- TTL ----------

  async alarm(): Promise<void> {
    const now = Date.now();
    this.dropExpired(now);
    const row = this.sql.exec<{ oldest: number | null }>("SELECT MIN(created_at) AS oldest FROM queue").one();
    if (row.oldest !== null) await this.ctx.storage.setAlarm(row.oldest + this.limits.queueTtlMs);
  }

  private async ensureAlarm(now: number): Promise<void> {
    if ((await this.ctx.storage.getAlarm()) === null) {
      await this.ctx.storage.setAlarm(now + this.limits.queueTtlMs);
    }
  }

  private dropExpired(now: number): void {
    this.sql.exec("DELETE FROM queue WHERE created_at <= ?", now - this.limits.queueTtlMs);
  }

  // ---------- helpers ----------

  private registry() {
    return this.env.REGISTRY.getByName(REGISTRY_NAME);
  }

  private liveSocket(): WebSocket | undefined {
    for (const ws of this.ctx.getWebSockets()) {
      const a = ws.deserializeAttachment() as Attachment | null;
      if (a && a.stage === "ready" && ws.readyState === WebSocket.READY_STATE_OPEN) return ws;
    }
    return undefined;
  }

  private isRegistered(): boolean {
    return this.getMeta("registered") === "1";
  }

  private nextSeq(): number {
    const cur = Number(this.getMeta("next_seq") ?? "1");
    this.setMeta("next_seq", String(cur + 1));
    return cur;
  }

  private getMeta(key: string): string | undefined {
    const rows = this.sql.exec<{ value: string }>("SELECT value FROM meta WHERE key = ?", key).toArray();
    return rows.length > 0 ? rows[0].value : undefined;
  }

  private setMeta(key: string, value: string): void {
    this.sql.exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, value);
  }

  // Token bucket for requests on the live connection (relay-v1 section 7): burst
  // requestBurst, refilled at requestRatePerSecond. It lives in memory, so it resets when the
  // object is evicted; that only makes the limit more generous after idle time.
  private takeToken(): boolean {
    if (!rateLimitsEnabled(this.env)) return true;
    const now = Date.now();
    const perMs = this.limits.requestRatePerSecond / 1000;
    this.tokens = Math.min(this.limits.requestBurst, this.tokens + (now - this.refilledAt) * perMs);
    this.refilledAt = now;
    if (this.tokens < 1) return false;
    this.tokens -= 1;
    return true;
  }
}

function deliverFrame(seq: number, from: string, id: string, frame: string): string {
  return JSON.stringify({ t: "deliver", seq, from, id, frame });
}
