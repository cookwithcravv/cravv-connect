import { DurableObject } from "cloudflare:workers";
import type { Env } from "./env";
import { b64decode, mailboxIdOf } from "./crypto";
import { readLimits, type Limits } from "./limits";
import { REGISTRY_NAME } from "./registry";
import { Schema } from "./schema";

export type BlobAction = "put" | "get" | "delete";

// HTTP status the handler should return; 0 means "authorized, continue".
export interface BlobDecision {
  status: 0 | 403 | 404 | 410;
  chunks: number;
}

type BlobRow = {
  blob_id: string;
  uploader: string;
  recipient: string;
  size: number;
  chunks: number;
  expires_at: number;
  state: string; // "active" | "expired"
};

const BLOB_DDL = [
  `CREATE TABLE IF NOT EXISTS blob (
    k INTEGER PRIMARY KEY CHECK (k = 1),
    blob_id TEXT NOT NULL,
    uploader TEXT NOT NULL,
    recipient TEXT NOT NULL,
    size INTEGER NOT NULL,
    chunks INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    state TEXT NOT NULL
  )`,
  "CREATE TABLE IF NOT EXISTS chunk (n INTEGER PRIMARY KEY, size INTEGER NOT NULL)",
];

export function chunkKey(blobId: string, n: number): string {
  return `blobs/${blobId}/${n}`;
}

// BlobMeta is one Durable Object per blob id. It stores who may touch the blob, enforces
// expiry, and removes the R2 chunks on delete or when the TTL alarm fires. After expiry it
// keeps a tombstone for one more TTL so late callers get 410 instead of 404. Tables exist
// only while a blob or its tombstone does: create makes them and the final delete removes
// all storage, so lookups of unknown blob ids store nothing.
export class BlobMeta extends DurableObject<Env> {
  private readonly sql: SqlStorage;
  private readonly limits: Limits;
  private readonly schema: Schema;

  constructor(ctx: DurableObjectState, env: Env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.limits = readLimits(env);
    this.schema = new Schema(this.sql, "blob", BLOB_DDL);
  }

  async create(blobId: string, uploader: string, recipient: string, size: number, chunks: number, expiresAt: number): Promise<boolean> {
    if (this.row()) return false;
    this.schema.ensure();
    this.sql.exec(
      "INSERT INTO blob (k, blob_id, uploader, recipient, size, chunks, expires_at, state) VALUES (1, ?, ?, ?, ?, ?, ?, 'active')",
      blobId,
      uploader,
      recipient,
      size,
      chunks,
      expiresAt,
    );
    await this.ctx.storage.setAlarm(expiresAt);
    return true;
  }

  // ik is the caller's canonical base64 IK.
  authorize(ik: string, action: BlobAction): BlobDecision {
    const r = this.row();
    if (!r) return { status: 404, chunks: 0 };
    if (r.state !== "active" || r.expires_at <= Date.now()) return { status: 410, chunks: r.chunks };
    const allowed =
      action === "put" ? ik === r.uploader : action === "get" ? ik === r.recipient : ik === r.uploader || ik === r.recipient;
    return { status: allowed ? 0 : 403, chunks: r.chunks };
  }

  // Records the size of chunk n before it is stored. False when the stored chunks would exceed
  // the declared size plus overhead per chunk (relay-v1 6.2). Re-uploads replace the old size.
  recordChunk(n: number, size: number): boolean {
    const r = this.row();
    if (!r) return false;
    const others = this.sql
      .exec<{ total: number }>("SELECT COALESCE(SUM(size), 0) AS total FROM chunk WHERE n != ?", n)
      .one().total;
    if (others + size > r.size + this.limits.chunkOverhead * r.chunks) return false;
    this.sql.exec("INSERT OR REPLACE INTO chunk (n, size) VALUES (?, ?)", n, size);
    return true;
  }

  // Called by a chunk PUT after its R2 write: 0 when the blob is still active and chunk n is
  // recorded, otherwise 404 (deleted) or 410 (expired), and the caller deletes its object.
  confirmChunk(n: number): 0 | 404 | 410 {
    const r = this.row();
    if (!r) return 404;
    if (r.state !== "active" || r.expires_at <= Date.now()) return 410;
    return this.sql.exec("SELECT 1 FROM chunk WHERE n = ?", n).toArray().length > 0 ? 0 : 404;
  }

  // Deletes chunks and metadata. Called after a successful authorize("delete"). The rows go
  // first, synchronously, so a chunk PUT that finishes during the R2 deletes sees the blob as
  // gone in confirmChunk and removes its own object.
  async destroy(): Promise<void> {
    const r = this.row();
    if (!r) return;
    this.sql.exec("DELETE FROM blob");
    this.sql.exec("DELETE FROM chunk");
    await this.removeChunks(r);
    await this.ctx.storage.deleteAlarm();
    await this.ctx.storage.deleteAll();
    this.schema.dropped();
  }

  async alarm(): Promise<void> {
    const r = this.row();
    if (!r) return;
    if (r.state === "active") {
      // Mark first so a concurrent chunk PUT sees 410 in confirmChunk (see destroy).
      this.sql.exec("UPDATE blob SET state = 'expired' WHERE k = 1");
      this.sql.exec("DELETE FROM chunk");
      await this.removeChunks(r);
      await this.ctx.storage.setAlarm(Date.now() + this.limits.blobTtlMs);
      return;
    }
    await this.ctx.storage.deleteAll();
    this.schema.dropped();
  }

  private async removeChunks(r: BlobRow): Promise<void> {
    const keys: string[] = [];
    for (let n = 0; n < r.chunks; n++) keys.push(chunkKey(r.blob_id, n));
    for (let i = 0; i < keys.length; i += 1000) await this.env.BLOBS.delete(keys.slice(i, i + 1000));
    const uploader = await mailboxIdOf(b64decode(r.uploader));
    await this.env.MAILBOX.getByName(uploader).releaseBlob(r.blob_id);
    await this.env.REGISTRY.getByName(REGISTRY_NAME).releaseStorage(r.blob_id);
  }

  private row(): BlobRow | undefined {
    if (!this.schema.exists()) return undefined;
    return this.sql
      .exec<BlobRow>(
        "SELECT blob_id, uploader, recipient, size, chunks, expires_at, state FROM blob WHERE k = 1",
      )
      .toArray()[0];
  }
}
