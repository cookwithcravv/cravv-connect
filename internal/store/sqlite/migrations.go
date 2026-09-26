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
	// v2: shared sessions, links, and the link every inbox item, task and
	// file travels on. Items, tasks and files from v1 keep link_id ''.
	`
CREATE TABLE shared_sessions (
	id              TEXT PRIMARY KEY,
	name            TEXT NOT NULL,
	purpose         TEXT NOT NULL,
	kind            TEXT NOT NULL,
	agent           TEXT NOT NULL,
	project_dir     TEXT NOT NULL,
	visibility_json TEXT NOT NULL,
	state           TEXT NOT NULL,
	reattach_hash   TEXT NOT NULL,
	wake_hash       TEXT NOT NULL,
	cursor          INTEGER NOT NULL,
	created_at      INTEGER NOT NULL,
	state_since     INTEGER NOT NULL
);
CREATE UNIQUE INDEX shared_sessions_live_name ON shared_sessions(name) WHERE state != 'closed';
CREATE INDEX shared_sessions_wake ON shared_sessions(wake_hash);
CREATE INDEX shared_sessions_reattach ON shared_sessions(reattach_hash);
CREATE TABLE links (
	num            INTEGER PRIMARY KEY AUTOINCREMENT,
	peer           TEXT NOT NULL,
	link_id        TEXT NOT NULL,
	direction      TEXT NOT NULL,
	session_id     TEXT NOT NULL,
	remote_session TEXT NOT NULL,
	remote_name    TEXT NOT NULL,
	remote_purpose TEXT NOT NULL,
	permission_in  TEXT NOT NULL,
	permission_out TEXT NOT NULL,
	proposed       TEXT NOT NULL,
	note           TEXT NOT NULL,
	state          TEXT NOT NULL,
	remote_away    INTEGER NOT NULL,
	reason         TEXT NOT NULL,
	created_at     INTEGER NOT NULL,
	updated_at     INTEGER NOT NULL,
	expires_at     INTEGER NOT NULL
);
CREATE UNIQUE INDEX links_peer_id ON links(peer, link_id);
CREATE INDEX links_session ON links(session_id, state);
ALTER TABLE inbox ADD COLUMN link_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN link_id TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN link_id TEXT NOT NULL DEFAULT '';
ALTER TABLE files ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
CREATE INDEX tasks_link ON tasks(link_id);
`,
	// v2 removes machine trust levels: pairings are kept, and what a peer may
	// do is set per link.
	`
ALTER TABLE peers DROP COLUMN trust_in;
`,
	// v1 leftovers: tasks and queued envelopes from before links carry no
	// link. Unfinished v1 tasks fail here with a note and are never sent
	// again (a v2 peer drops link-less traffic), and queued chat, task.* and
	// file.offer envelopes without a link_id are deleted.
	`
UPDATE tasks SET
	state = 'failed',
	expires_at = 0,
	updated_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER),
	notes_json = json_insert(CASE WHEN json_valid(notes_json) THEN notes_json ELSE '[]' END, '$[#]',
		json_object('at', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), 'text', 'no_link_after_upgrade'))
WHERE link_id = '' AND state IN ('sent', 'awaiting_approval', 'queued', 'seen', 'claimed', 'running');
DELETE FROM outbox WHERE id IN (
	SELECT id FROM (
		SELECT id, CASE WHEN json_valid(CAST(envelope AS TEXT)) THEN CAST(envelope AS TEXT) END AS env FROM outbox
	) WHERE env IS NOT NULL
		AND json_extract(env, '$.kind') IN ('chat', 'task.create', 'task.update', 'task.cancel', 'file.offer')
		AND COALESCE(json_extract(env, '$.link_id'), '') = ''
);
`,
	// v2 Phase 3: managed-session offers per paired machine, what the daemon
	// keeps about each managed session, and run starts for the caps.
	`
CREATE TABLE offers (
	id              TEXT PRIMARY KEY,
	peer            TEXT NOT NULL,
	label           TEXT NOT NULL,
	folder          TEXT NOT NULL,
	real_folder     TEXT NOT NULL,
	agent           TEXT NOT NULL,
	permission      TEXT NOT NULL,
	run_mode        TEXT NOT NULL,
	max_concurrent  INTEGER NOT NULL,
	idle_timeout_ms INTEGER NOT NULL,
	max_turns       INTEGER NOT NULL,
	run_timeout_ms  INTEGER NOT NULL,
	runs_per_hour   INTEGER NOT NULL,
	runs_per_day    INTEGER NOT NULL,
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX offers_peer_label ON offers(peer, label);
CREATE TABLE managed_sessions (
	session_id    TEXT PRIMARY KEY REFERENCES shared_sessions(id) ON DELETE CASCADE,
	offer_id      TEXT NOT NULL,
	peer          TEXT NOT NULL,
	link_id       TEXT NOT NULL,
	agent_session TEXT NOT NULL,
	started       INTEGER NOT NULL,
	last_active   INTEGER NOT NULL,
	created_at    INTEGER NOT NULL
);
CREATE TABLE managed_runs (
	id         TEXT PRIMARY KEY,
	session_id TEXT NOT NULL,
	peer       TEXT NOT NULL,
	link_id    TEXT NOT NULL,
	started_at INTEGER NOT NULL
);
CREATE INDEX managed_runs_peer ON managed_runs(peer, started_at);
CREATE INDEX managed_runs_link ON managed_runs(link_id, started_at);
`,
	// v2 never holds incoming files for a human, and `files accept` is gone:
	// every held file is declined locally. One held by v1 has no link
	// (no_link_after_upgrade, like v1 tasks); one held by an earlier v2 build
	// is on a link (held_files_retired). Nothing is sent: the peer learns
	// nothing it could act on, and the file shows as declined here.
	`
UPDATE files SET
	state = 'declined',
	reason = CASE WHEN link_id = '' THEN 'no_link_after_upgrade' ELSE 'held_files_retired' END
WHERE state = 'held' AND direction = 'in';
`,
	// Presence marks a link away when its peer machine stops answering
	// (0: answering), and closes it only after the away grace.
	`
ALTER TABLE links ADD COLUMN presence_away_at INTEGER NOT NULL DEFAULT 0;
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
