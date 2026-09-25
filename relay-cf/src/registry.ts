import { DurableObject } from "cloudflare:workers";
import { constantTimeEqual, randomToken } from "./crypto";
import type { Env } from "./env";
import { readLimits, type Limits } from "./limits";

export const REGISTRY_NAME = "registry";

// Registry is a single Durable Object instance (idFromName("registry")) that owns relay
// membership, single-use invites, and per-member blob storage accounting.
export class Registry extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  private readonly limits: Limits;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.limits = readLimits(env);
    this.sql.exec(`CREATE TABLE IF NOT EXISTS members (
      mailbox_id TEXT PRIMARY KEY,
      created_at INTEGER NOT NULL
    )`);
    this.sql.exec(`CREATE TABLE IF NOT EXISTS invites (
      token TEXT PRIMARY KEY,
      issuer TEXT NOT NULL,
      expires_at INTEGER NOT NULL
    )`);
    this.sql.exec(`CREATE TABLE IF NOT EXISTS blob_usage (
      blob_id TEXT PRIMARY KEY,
      mailbox_id TEXT NOT NULL,
      size INTEGER NOT NULL,
      expires_at INTEGER NOT NULL
    )`);
  }

  isMember(mailboxId: string): boolean {
    return this.sql.exec("SELECT 1 FROM members WHERE mailbox_id = ?", mailboxId).toArray().length > 0;
  }

  // Registers mailboxId when the admin token matches or the invite is valid and unused.
  // An invite is consumed on success. Already-registered mailboxes succeed idempotently.
  async register(mailboxId: string, adminToken?: string, invite?: string): Promise<boolean> {
    if (this.isMember(mailboxId)) return true;
    const now = Date.now();
    let ok = false;
    if (adminToken && this.env.ADMIN_TOKEN) {
      ok = await constantTimeEqual(adminToken, this.env.ADMIN_TOKEN);
    }
    if (!ok && invite) {
      const rows = this.sql
        .exec("DELETE FROM invites WHERE token = ? AND expires_at > ? RETURNING token", invite, now)
        .toArray();
      ok = rows.length === 1;
    }
    if (!ok) return false;
    this.sql.exec("INSERT OR IGNORE INTO members (mailbox_id, created_at) VALUES (?, ?)", mailboxId, now);
    return true;
  }

  // Issues a single-use invite valid for the invite TTL. Returns null if issuer is not a member.
  createInvite(issuer: string): string | null {
    if (!this.isMember(issuer)) return null;
    const now = Date.now();
    this.sql.exec("DELETE FROM invites WHERE expires_at <= ?", now);
    const token = randomToken();
    this.sql.exec(
      "INSERT INTO invites (token, issuer, expires_at) VALUES (?, ?, ?)",
      token,
      issuer,
      now + this.limits.inviteTtlMs,
    );
    return token;
  }

  // Reserves blob storage against the uploader's quota. False when the quota would be exceeded.
  reserveBlob(mailboxId: string, blobId: string, size: number, expiresAt: number): boolean {
    const now = Date.now();
    this.sql.exec("DELETE FROM blob_usage WHERE expires_at <= ?", now);
    const row = this.sql
      .exec<{ used: number }>("SELECT COALESCE(SUM(size), 0) AS used FROM blob_usage WHERE mailbox_id = ?", mailboxId)
      .one();
    if (row.used + size > this.limits.blobQuotaBytes) return false;
    this.sql.exec(
      "INSERT INTO blob_usage (blob_id, mailbox_id, size, expires_at) VALUES (?, ?, ?, ?)",
      blobId,
      mailboxId,
      size,
      expiresAt,
    );
    return true;
  }

  releaseBlob(blobId: string): void {
    this.sql.exec("DELETE FROM blob_usage WHERE blob_id = ?", blobId);
  }
}
