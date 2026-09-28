import type { Limits } from "./limits";

// Tables of a Mailbox DO's meta and queue. The Mailbox creates them on its first write
// (see schema.ts); Meta and Queue assume they exist.
export const META_DDL = "CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)";
export const QUEUE_DDL = `CREATE TABLE IF NOT EXISTS queue (
  seq INTEGER PRIMARY KEY,
  from_ik TEXT NOT NULL,
  id TEXT NOT NULL,
  frame TEXT NOT NULL,
  size INTEGER NOT NULL,
  created_at INTEGER NOT NULL
)`;

// Meta is a tiny key/value table inside a Mailbox DO (registered flag, seq counter, ...).
export class Meta {
  constructor(private readonly sql: SqlStorage) {}

  get(key: string): string | undefined {
    const rows = this.sql.exec<{ value: string }>("SELECT value FROM meta WHERE key = ?", key).toArray();
    return rows.length > 0 ? rows[0].value : undefined;
  }

  set(key: string, value: string): void {
    this.sql.exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", key, value);
  }
}

export interface QueuedFrame {
  seq: number;
  from_ik: string;
  id: string;
  frame: string;
}

// Queue is a mailbox's persistent frame queue (relay-v1 sections 3.5, 3.6 and 4). seq is
// allocated from a persistent counter so it is never reused, even after the queue empties.
export class Queue {
  constructor(
    private readonly sql: SqlStorage,
    private readonly meta: Meta,
    private readonly limits: Limits,
  ) {}

  // Removes frames older than the queue TTL.
  dropExpired(now: number): void {
    this.sql.exec("DELETE FROM queue WHERE created_at <= ?", now - this.limits.queueTtlMs);
  }

  // True when one more frame of size bytes would exceed a cap. Call dropExpired first.
  wouldOverflow(size: number): boolean {
    const stats = this.sql
      .exec<{ n: number; bytes: number }>("SELECT COUNT(*) AS n, COALESCE(SUM(size), 0) AS bytes FROM queue")
      .one();
    return stats.n + 1 > this.limits.queueMaxFrames || stats.bytes + size > this.limits.queueMaxBytes;
  }

  // Appends a frame with the next seq and returns that seq.
  append(fromIk: string, id: string, frame: string, size: number, now: number): number {
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
    return seq;
  }

  // Every queued frame in ascending seq.
  backlog(): Iterable<QueuedFrame> {
    return this.sql.exec<{ seq: number; from_ik: string; id: string; frame: string }>(
      "SELECT seq, from_ik, id, frame FROM queue ORDER BY seq",
    );
  }

  ack(seq: number): void {
    this.sql.exec("DELETE FROM queue WHERE seq <= ?", seq);
  }

  // created_at of the oldest queued frame, or null when the queue is empty.
  oldest(): number | null {
    return this.sql.exec<{ oldest: number | null }>("SELECT MIN(created_at) AS oldest FROM queue").one().oldest;
  }

  private nextSeq(): number {
    const cur = Number(this.meta.get("next_seq") ?? "1");
    this.meta.set("next_seq", String(cur + 1));
    return cur;
  }
}
