import { DurableObject } from "cloudflare:workers";
import { constantTimeEqual, randomToken } from "./crypto";
import type { Env } from "./env";
import { readLimits, type Limits } from "./limits";
import { Code } from "./protocol";

export const REGISTRY_NAME = "registry";

export type InviteResult =
  | { ok: true; invite: string }
  | { ok: false; code: typeof Code.FORBIDDEN | typeof Code.RATE_LIMITED };

// Registry is a single Durable Object instance (idFromName("registry")) that owns relay
// membership, single-use invites, and the relay-wide total of live blob bytes. It is only
// reached for registration, invites, and blob create/release: per-request membership checks
// go to the caller's own Mailbox DO, so throwaway keys cannot flood this one instance.
export class Registry extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  private readonly limits: Limits;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.limits = readLimits(env);
    this.sql.exec(`CREATE TABLE IF NOT EXISTS members (
      mailbox_id TEXT PRIMARY KEY,
      created_at INTEGER NOT NULL,
      invited_by TEXT
    )`);
    const cols = this.sql.exec<{ name: string }>("PRAGMA table_info(members)").toArray();
    if (!cols.some((c) => c.name === "invited_by")) this.sql.exec("ALTER TABLE members ADD COLUMN invited_by TEXT");
    this.sql.exec(`CREATE TABLE IF NOT EXISTS invites (
      token TEXT PRIMARY KEY,
      issuer TEXT NOT NULL,
      expires_at INTEGER NOT NULL
    )`);
    this.sql.exec("CREATE INDEX IF NOT EXISTS invites_issuer ON invites (issuer)");
    this.sql.exec(`CREATE TABLE IF NOT EXISTS blob_storage (
      blob_id TEXT PRIMARY KEY,
      size INTEGER NOT NULL,
      expires_at INTEGER NOT NULL
    )`);
  }

  isMember(mailboxId: string): boolean {
    return this.sql.exec("SELECT 1 FROM members WHERE mailbox_id = ?", mailboxId).toArray().length > 0;
  }

  // Registers mailboxId when the admin token matches or the invite is valid and unused.
  // An invite is consumed on success and its issuer is recorded as invited_by. Already
  // registered mailboxes succeed idempotently.
  async register(mailboxId: string, adminToken?: string, invite?: string): Promise<boolean> {
    if (this.isMember(mailboxId)) return true;
    const now = Date.now();
    let ok = false;
    let invitedBy: string | null = null;
    if (adminToken && this.env.ADMIN_TOKEN) {
      ok = await constantTimeEqual(adminToken, this.env.ADMIN_TOKEN);
    }
    if (!ok && invite) {
      const rows = this.sql
        .exec<{ issuer: string }>("DELETE FROM invites WHERE token = ? AND expires_at > ? RETURNING issuer", invite, now)
        .toArray();
      ok = rows.length === 1;
      if (ok) invitedBy = rows[0].issuer;
    }
    if (!ok) return false;
    this.sql.exec(
      "INSERT OR IGNORE INTO members (mailbox_id, created_at, invited_by) VALUES (?, ?, ?)",
      mailboxId,
      now,
      invitedBy,
    );
    return true;
  }

  // Issues a single-use invite valid for the invite TTL. Fails with forbidden if issuer is not
  // a member and rate_limited if it already holds maxOutstandingInvites unexpired invites.
  createInvite(issuer: string): InviteResult {
    if (!this.isMember(issuer)) return { ok: false, code: Code.FORBIDDEN };
    const now = Date.now();
    this.sql.exec("DELETE FROM invites WHERE expires_at <= ?", now);
    const held = this.sql.exec<{ n: number }>("SELECT COUNT(*) AS n FROM invites WHERE issuer = ?", issuer).one().n;
    if (held >= this.limits.maxOutstandingInvites) return { ok: false, code: Code.RATE_LIMITED };
    const token = randomToken();
    this.sql.exec(
      "INSERT INTO invites (token, issuer, expires_at) VALUES (?, ?, ?)",
      token,
      issuer,
      now + this.limits.inviteTtlMs,
    );
    return { ok: true, invite: token };
  }

  // Reserves size bytes of relay-wide blob storage. False when the relay total would exceed
  // maxTotalBlobBytes. Expired reservations never count.
  reserveStorage(blobId: string, size: number, expiresAt: number): boolean {
    this.sql.exec("DELETE FROM blob_storage WHERE expires_at <= ?", Date.now());
    const used = this.sql
      .exec<{ used: number }>("SELECT COALESCE(SUM(size), 0) AS used FROM blob_storage")
      .one().used;
    if (used + size > this.limits.maxTotalBlobBytes) return false;
    this.sql.exec(
      "INSERT OR REPLACE INTO blob_storage (blob_id, size, expires_at) VALUES (?, ?, ?)",
      blobId,
      size,
      expiresAt,
    );
    return true;
  }

  releaseStorage(blobId: string): void {
    this.sql.exec("DELETE FROM blob_storage WHERE blob_id = ?", blobId);
  }
}
