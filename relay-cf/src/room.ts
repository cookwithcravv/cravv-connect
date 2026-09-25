import { DurableObject } from "cloudflare:workers";
import { b64decode, sha256Hex } from "./crypto";
import type { Env } from "./env";
import { readLimits, type Limits } from "./limits";
import { CLOSE_NORMAL, Code, failSocket, parseFrame, str, type ErrorCode } from "./protocol";

type Role = "creator" | "joiner";

type RoomRow = {
  token_hash: string;
  expires_at: number;
  joined: number;
};

// Room is one Durable Object per pairing nameplate (relay-v1 section 5). It relays opaque PAKE
// messages between exactly one creator (holding the creator token) and exactly one joiner,
// then burns itself. Each socket is tagged with its role and with a generation tag (a prefix
// of the room's token hash) so late close events from an old room never touch a new room
// that reuses the nameplate.
export class Room extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  private readonly limits: Limits;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.limits = readLimits(env);
    this.migrate();
  }

  private migrate(): void {
    this.sql.exec(`CREATE TABLE IF NOT EXISTS room (
      k INTEGER PRIMARY KEY CHECK (k = 1),
      token_hash TEXT NOT NULL,
      expires_at INTEGER NOT NULL,
      joined INTEGER NOT NULL DEFAULT 0
    )`);
    this.sql.exec(`CREATE TABLE IF NOT EXISTS pending (
      n INTEGER PRIMARY KEY AUTOINCREMENT,
      data TEXT NOT NULL
    )`);
  }

  // RPC from the creating Mailbox DO. Returns false when the nameplate is already in use.
  async init(tokenHash: string): Promise<boolean> {
    const now = Date.now();
    const cur = this.room();
    if (cur && cur.expires_at > now) return false;
    if (cur) await this.burn();
    const expiresAt = now + this.limits.roomTtlMs;
    this.sql.exec("INSERT INTO room (k, token_hash, expires_at, joined) VALUES (1, ?, ?, 0)", tokenHash, expiresAt);
    await this.ctx.storage.setAlarm(expiresAt);
    return true;
  }

  async fetch(request: Request): Promise<Response> {
    if (request.headers.get("Upgrade")?.toLowerCase() !== "websocket") {
      return Response.json({ code: Code.BAD_REQUEST, message: "expected websocket upgrade" }, { status: 426 });
    }
    const token = new URL(request.url).searchParams.get("token");
    const r = this.room();
    if (!r) return reject(Code.NOT_FOUND, "no such pairing room");
    if (r.expires_at <= Date.now()) {
      await this.burn();
      return reject(Code.GONE, "pairing room expired");
    }
    const gen = genTag(r);

    if (token !== null && token !== "") {
      if ((await sha256Hex(token)) !== r.token_hash) return reject(Code.FORBIDDEN, "wrong creator token");
      if (this.sockets("creator", gen).length > 0) return reject(Code.GONE, "creator already connected");
      const { client, server } = this.accept("creator", gen);
      server.send(JSON.stringify({ t: "waiting" }));
      return new Response(null, { status: 101, webSocket: client });
    }

    const creators = this.sockets("creator", gen);
    if (creators.length === 0) return reject(Code.NOT_FOUND, "pairing room not open yet");
    if (r.joined) return reject(Code.GONE, "pairing room already used");
    this.sql.exec("UPDATE room SET joined = 1 WHERE k = 1");
    const { client, server } = this.accept("joiner", gen);
    const joined = JSON.stringify({ t: "peer_joined" });
    server.send(joined);
    const buffered = this.sql.exec<{ data: string }>("SELECT data FROM pending ORDER BY n").toArray();
    for (const b of buffered) server.send(JSON.stringify({ t: "msg", data: b.data }));
    this.sql.exec("DELETE FROM pending");
    creators[0].send(joined);
    return new Response(null, { status: 101, webSocket: client });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    const r = this.room();
    if (!r || this.genOf(ws) !== genTag(r)) {
      failSocket(ws, Code.GONE, "pairing room ended");
      return;
    }
    if (r.expires_at <= Date.now()) {
      await this.expire();
      return;
    }
    const f = typeof message === "string" ? parseFrame(message) : null;
    const data = f && f.t === "msg" ? str(f.data) : undefined;
    if (data === undefined) return this.fail(ws, Code.BAD_REQUEST, "expected msg");
    let size: number;
    try {
      size = b64decode(data).length;
    } catch {
      return this.fail(ws, Code.BAD_REQUEST, "data is not base64");
    }
    if (size > this.limits.maxRoomMessageBytes) return this.fail(ws, Code.TOO_LARGE, "room message too large");

    const other = otherRole(this.roleOf(ws));
    const peers = this.sockets(other, genTag(r));
    if (peers.length > 0) {
      peers[0].send(JSON.stringify({ t: "msg", data }));
      return;
    }
    const n = this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM pending").one().n;
    if (n >= this.limits.roomBufferMax) return this.fail(ws, Code.BAD_REQUEST, "too many messages before the peer joined");
    this.sql.exec("INSERT INTO pending (data) VALUES (?)", data);
  }

  async webSocketClose(ws: WebSocket): Promise<void> {
    await this.leave(ws);
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    await this.leave(ws);
  }

  async alarm(): Promise<void> {
    await this.expire();
  }

  // A side broke the rules: it gets error{code}, the other side gets closed, the room burns.
  private async fail(ws: WebSocket, code: ErrorCode, message: string): Promise<void> {
    this.notifyOthers(ws);
    failSocket(ws, code, message);
    await this.burn();
  }

  // A side disconnected: the other side gets closed, the room burns.
  private async leave(ws: WebSocket): Promise<void> {
    try {
      ws.close(CLOSE_NORMAL, "closed");
    } catch {
      // already closed
    }
    const r = this.room();
    if (!r || this.genOf(ws) !== genTag(r)) return;
    this.notifyOthers(ws);
    await this.burn();
  }

  // Lifetime over: every connected side gets error{gone}, the room burns.
  private async expire(): Promise<void> {
    const r = this.room();
    if (r) for (const ws of this.ctx.getWebSockets(genTag(r))) failSocket(ws, Code.GONE, "pairing room expired");
    await this.burn();
  }

  private notifyOthers(ws: WebSocket): void {
    const gen = this.genOf(ws);
    for (const peer of this.ctx.getWebSockets(gen)) {
      if (peer === ws) continue;
      try {
        peer.send(JSON.stringify({ t: "closed" }));
        peer.close(CLOSE_NORMAL, "closed");
      } catch {
        // already closed
      }
    }
  }

  private async burn(): Promise<void> {
    await this.ctx.storage.deleteAlarm();
    await this.ctx.storage.deleteAll();
    this.migrate();
  }

  private accept(role: Role, gen: string): { client: WebSocket; server: WebSocket } {
    const pair = new WebSocketPair();
    const [client, server] = Object.values(pair);
    this.ctx.acceptWebSocket(server, [role, gen]);
    return { client, server };
  }

  private sockets(role: Role, gen: string): WebSocket[] {
    return this.ctx
      .getWebSockets(role)
      .filter((ws) => this.genOf(ws) === gen && ws.readyState === WebSocket.READY_STATE_OPEN);
  }

  private roleOf(ws: WebSocket): Role {
    return this.ctx.getTags(ws).includes("creator") ? "creator" : "joiner";
  }

  private genOf(ws: WebSocket): string {
    return this.ctx.getTags(ws).find((t) => t.startsWith("g:")) ?? "";
  }

  private room(): RoomRow | undefined {
    return this.sql
      .exec<RoomRow>("SELECT token_hash, expires_at, joined FROM room WHERE k = 1")
      .toArray()[0];
  }
}

function genTag(r: RoomRow): string {
  return `g:${r.token_hash.slice(0, 16)}`;
}

function otherRole(r: Role): Role {
  return r === "creator" ? "joiner" : "creator";
}

// Accepts the upgrade only to deliver a protocol error frame, then closes.
function reject(code: ErrorCode, message: string): Response {
  const pair = new WebSocketPair();
  const [client, server] = Object.values(pair);
  server.accept();
  failSocket(server, code, message);
  return new Response(null, { status: 101, webSocket: client });
}
