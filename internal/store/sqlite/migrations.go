package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// migrations are applied in order; index i is schema version i+1.
// Never edit a released migration: append a new one.
var migrations = []string{
	`
CREATE TABLE peers (
	machine_id     TEXT PRIMARY KEY,
	ik             BLOB NOT NULL,
	alias          TEXT NOT NULL UNIQUE,
	trust_in       INTEGER NOT NULL,
	prekey_json    TEXT NOT NULL,
	relay_url      TEXT NOT NULL,
	paused         INTEGER NOT NULL DEFAULT 0,
	paused_by_peer INTEGER NOT NULL DEFAULT 0,
	paired_at      INTEGER NOT NULL
);
CREATE TABLE prekeys (
	id            TEXT PRIMARY KEY,
	priv          BLOB NOT NULL,
	created_at    INTEGER NOT NULL,
	superseded_at INTEGER
);
CREATE TABLE outbox (
	id           TEXT PRIMARY KEY,
	to_machine   TEXT NOT NULL,
	envelope     BLOB NOT NULL,
	status       TEXT NOT NULL,
	attempts     INTEGER NOT NULL,
	next_attempt INTEGER NOT NULL,
	created_at   INTEGER NOT NULL
);
CREATE INDEX outbox_due ON outbox(status, next_attempt);
CREATE INDEX outbox_to ON outbox(to_machine);
CREATE TABLE inbox (
	seq          INTEGER PRIMARY KEY AUTOINCREMENT,
	msg_id       TEXT NOT NULL,
	from_machine TEXT NOT NULL,
	from_session TEXT NOT NULL,
	to_session   TEXT NOT NULL,
	kind         TEXT NOT NULL,
	body         BLOB NOT NULL,
	task_id      TEXT NOT NULL,
	note         TEXT NOT NULL,
	received_at  INTEGER NOT NULL,
	read_by_any  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX inbox_to_session ON inbox(to_session, seq);
CREATE TABLE sessions (
	name        TEXT PRIMARY KEY,
	agent       TEXT NOT NULL,
	project_dir TEXT NOT NULL,
	cursor      INTEGER NOT NULL,
	last_seen   INTEGER NOT NULL,
	connected   INTEGER NOT NULL
);
CREATE TABLE tasks (
	id                TEXT PRIMARY KEY,
	direction         TEXT NOT NULL,
	peer              TEXT NOT NULL,
	from_session      TEXT NOT NULL,
	to_session        TEXT NOT NULL,
	instructions      TEXT NOT NULL,
	state             TEXT NOT NULL,
	claimed_by        TEXT NOT NULL,
	notes_json        TEXT NOT NULL,
	result            TEXT NOT NULL,
	result_files_json TEXT NOT NULL,
	files_json        TEXT NOT NULL,
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL,
	expires_at        INTEGER NOT NULL
);
CREATE INDEX tasks_state ON tasks(state);
CREATE TABLE files (
	file_id    TEXT PRIMARY KEY,
	direction  TEXT NOT NULL,
	peer       TEXT NOT NULL,
	msg_id     TEXT NOT NULL,
	blob_id    TEXT NOT NULL,
	name       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	chunks     INTEGER NOT NULL,
	sha256     BLOB,
	key        BLOB,
	task_id    TEXT NOT NULL,
	state      TEXT NOT NULL,
	local_path TEXT NOT NULL,
	next_chunk INTEGER NOT NULL,
	attempts   INTEGER NOT NULL,
	reason     TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE dedup (
	id      TEXT PRIMARY KEY,
	seen_at INTEGER NOT NULL
);
CREATE INDEX dedup_seen ON dedup(seen_at);
CREATE TABLE settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`,
	`
CREATE INDEX inbox_msg ON inbox(msg_id);
CREATE INDEX files_created ON files(created_at);
`,
	// A chat is stored once per (msg_id, to_session), so a redelivery after a
	// crash between the insert and the dedup mark cannot store it twice. Other
	// kinds are made idempotent by their own records (tasks, files), and file
	// notices legitimately repeat a message ID. Duplicates an older version
	// stored are removed first, keeping the earliest.
	`
DELETE FROM inbox WHERE kind = 'chat' AND msg_id != '' AND seq NOT IN (
	SELECT MIN(seq) FROM inbox WHERE kind = 'chat' AND msg_id != '' GROUP BY msg_id, to_session);
CREATE UNIQUE INDEX inbox_chat_once ON inbox(msg_id, to_session) WHERE kind = 'chat' AND msg_id != '';
`,
}

// migrate creates schema_migrations and applies every migration whose
// version is not yet recorded, each in its own transaction.
func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	var current int
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("store: database schema version %d is newer than this binary (%d)", current, len(migrations))
	}
	for v := current + 1; v <= len(migrations); v++ {
		err := inTx(ctx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, migrations[v-1]); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, v)
			return err
		})
		if err != nil {
			return fmt.Errorf("store: apply migration %d: %w", v, err)
		}
	}
	return nil
}
