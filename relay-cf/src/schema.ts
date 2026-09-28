// Schema creates a Durable Object's tables on its first write instead of in the constructor.
// Merely addressing an object (a connect or signed blob request from an unregistered key, a
// join on an unknown nameplate) then never persists storage, so throwaway keys and random
// names cannot leave empty objects behind forever. Read paths call exists() and treat a
// missing schema as "nothing stored".
export class Schema {
  private present: boolean | undefined;
  private created = false;

  // probe is a table every write path creates; ddl is idempotent (CREATE ... IF NOT EXISTS).
  constructor(
    private readonly sql: SqlStorage,
    private readonly probe: string,
    private readonly ddl: readonly string[],
  ) {}

  // True when the tables exist. Looked up once per instance, then kept in memory.
  exists(): boolean {
    this.present ??=
      this.sql.exec("SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?", this.probe).toArray().length > 0;
    return this.present;
  }

  // Creates any missing table. The DDL runs once per instance, so an object created by an
  // older version picks up tables added since.
  ensure(): void {
    if (this.created) return;
    for (const stmt of this.ddl) this.sql.exec(stmt);
    this.created = true;
    this.present = true;
  }

  // Call after ctx.storage.deleteAll(): the tables are gone and are not recreated until the
  // next write.
  dropped(): void {
    this.created = false;
    this.present = false;
  }
}
