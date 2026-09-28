import { DurableObject } from "cloudflare:workers";
import { b64decode, sha256Hex } from "./crypto";
import type { Env } from "./env";
import { isUpgrade, notUpgrade, rejectSocket } from "./http";
import { readLimits, type Limits } from "./limits";
import { CLOSE_NORMAL, Code, failSocket, parseFrame, str, type ErrorCode } from "./protocol";
import { describeError } from "./rpc";
import { Schema } from "./schema";

type Role = "creator" | "joiner";

type RoomRow = {
  token_hash: string;
  expires_at: number;
  joined: number;
};

const ROOM_DDL = [
  `CREATE TABLE IF NOT EXISTS room (
    k INTEGER PRIMARY KEY CHECK (k = 1),
    token_hash TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    joined INTEGER NOT NULL DEFAULT 0
  )`,
  `CREATE TABLE IF NOT EXISTS pending (
    n INTEGER PRIMARY KEY AUTOINCREMENT,
    data TEXT NOT NULL
  )`,
  // The creating member's mailbox id, told when the room burns so its room cap frees up.
  // A table of its own so rooms created before it existed keep their row shape.
  `CREATE TABLE IF NOT EXISTS owner (
    k INTEGER PRIMARY KEY CHECK (k = 1),
    mailbox_id TEXT NOT NULL
  )`,
];

// Room is one Durable Object per pairing nameplate (relay-v1 section 5). It relays opaque PAKE
// messages between exactly one creator (holding the creator token) and exactly one joiner,
// then burns itself. Each socket is tagged with its role and with a generation tag (a prefix
// of the room's token hash) so late close events from an old room never touch a new room
// that reuses the nameplate. Tables exist only while a room does: init creates them and a
// burn deletes all storage, so joins on unknown nameplates store nothing.
export class Room extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  private readonly limits: Limits;
  private readonly schema: Schema;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.limits = readLimits(env);
    this.schema = new Schema(this.sql, "room", ROOM_DDL);
  }

  // RPC from the creating Mailbox DO (owner is its mailbox id). Returns false when the
  // nameplate is already in use.
  async init(tokenHash: string, owner: string): Promise<boolean> {
    const now = Date.now();
    const cur = this.room();
    if (cur && cur.expires_at > now) return false;
    if (cur) {
      await this.burn();
      // burn waits on the old owner's mailbox, so another init may have taken the nameplate.
      if (this.room()) return false;
    }
    const expiresAt = now + this.limits.roomTtlMs;
    this.schema.ensure();
    this.sql.exec("INSERT INTO room (k, token_hash, expires_at, joined) VALUES (1, ?, ?, 0)", tokenHash, expiresAt);
    this.sql.exec("INSERT OR REPLACE INTO owner (k, mailbox_id) VALUES (1, ?)", owner);
    await this.ctx.storage.setAlarm(expiresAt);
    return true;
  }

  async fetch(request: Request): Promise<Response> {
    if (!isUpgrade(request)) return notUpgrade();
    const token = new URL(request.url).searchParams.get("token");
    const r = this.room();
    if (!r) return rejectSocket(Code.NOT_FOUND, "no such pairing room");
    if (r.expires_at <= Date.now()) {
      await this.burn();
      return rejectSocket(Code.GONE, "pairing room expired");
    }
    const gen = genTag(r);

    if (token !== null && token !== "") {
      if ((await sha256Hex(token)) !== r.token_hash) return rejectSocket(Code.FORBIDDEN, "wrong creator token");
      if (this.sockets("creator", gen).length > 0) return rejectSocket(Code.GONE, "creator already connected");
      const { client, server } = this.accept("creator", gen);
      server.send(JSON.stringify({ t: "waiting" }));
      return new Response(null, { status: 101, webSocket: client });
    }

    const creators = this.sockets("creator", gen);
    if (creators.length === 0) return rejectSocket(Code.NOT_FOUND, "pairing room not open yet");
    if (r.joined) return rejectSocket(Code.GONE, "pairing room already used");
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

  // Deletes the room and all its storage, then frees the owner's room slot.
  private async burn(): Promise<void> {
    const r = this.room();
    const owner = r ? this.owner() : undefined;
    await this.ctx.storage.deleteAlarm();
    await this.ctx.storage.deleteAll();
    this.schema.dropped();
    if (!r || !owner) return;
    try {
      await this.env.MAILBOX.getByName(owner).releaseRoom(r.token_hash);
    } catch (err) {
      // The slot still frees when the room's lifetime ends.
      console.error("room: releasing the owner's room slot failed:", describeError(err));
    }
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
    if (!this.schema.exists()) return undefined;
    return this.sql
      .exec<RoomRow>("SELECT token_hash, expires_at, joined FROM room WHERE k = 1")
      .toArray()[0];
  }

  // Undefined for rooms created before the owner was recorded.
  private owner(): string | undefined {
    const hasTable =
      this.sql.exec("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'owner'").toArray().length > 0;
    if (!hasTable) return undefined;
    return this.sql.exec<{ mailbox_id: string }>("SELECT mailbox_id FROM owner WHERE k = 1").toArray()[0]?.mailbox_id;
  }
}

function genTag(r: RoomRow): string {
  return `g:${r.token_hash.slice(0, 16)}`;
}

function otherRole(r: Role): Role {
  return r === "creator" ? "joiner" : "creator";
}
