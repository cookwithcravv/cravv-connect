package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// t0 is a fixed base time for all store tests (UTC, whole milliseconds).
var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenCreatesFileWith0600AndWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	var mode string
	if err := db.sql.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var fk, busy int
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if err := db.sql.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if fk != 1 || busy != 5000 {
		t.Fatalf("foreign_keys=%d busy_timeout=%d, want 1 and 5000", fk, busy)
	}
}

func TestReopenKeepsDataAndSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	v, ok, err := db2.GetSetting(context.Background(), "k")
	if err != nil || !ok || v != "v" {
		t.Fatalf("after reopen got %q %v %v", v, ok, err)
	}
	var n, maxV int
	if err := db2.sql.QueryRow(`SELECT COUNT(*), MAX(version) FROM schema_migrations`).Scan(&n, &maxV); err != nil {
		t.Fatal(err)
	}
	if n != len(migrations) || maxV != len(migrations) {
		t.Fatalf("schema_migrations rows=%d max=%d, want %d", n, maxV, len(migrations))
	}
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, len(migrations)+1); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("Open succeeded on a database from a newer binary")
	}
}

func TestTimesRoundTripAsUnixMillis(t *testing.T) {
	cases := []time.Time{{}, t0, t0.Add(1500 * time.Microsecond)}
	for _, c := range cases {
		got := fromMS(toMS(c))
		want := c.Truncate(time.Millisecond)
		if c.IsZero() {
			if !got.IsZero() {
				t.Fatalf("zero time came back as %v", got)
			}
			continue
		}
		if !got.Equal(want) {
			t.Fatalf("round trip %v -> %v, want %v", c, got, want)
		}
	}
}

// Transactions must take the write lock at BEGIN (BEGIN IMMEDIATE), so a
// second process cannot slip a write in between the read and the write of a
// read-check-write transaction such as Transition.
func TestTransactionsBeginImmediate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.Ping(); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// No statement has run in tx yet: a deferred BEGIN would hold no lock.
	_, err = other.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('k', 'v')`)
	if err == nil {
		t.Fatal("another connection wrote while a transaction was open; BEGIN is not IMMEDIATE")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "locked") && !strings.Contains(strings.ToLower(err.Error()), "busy") {
		t.Fatalf("unexpected error from competing writer: %v", err)
	}
}

// The migration that makes chat inserts idempotent first removes duplicate
// chat rows an older version may have stored, keeping the earliest.
func TestChatUniqueMigrationDropsDuplicates(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	raw := openAtVersion(t, path, 2) // the schema before inbox_chat_once
	for _, id := range []string{"M1", "M1", "M2"} {
		if _, err := raw.ExecContext(ctx, `INSERT INTO inbox (msg_id, from_machine, from_session, to_session, kind, body, task_id, note, received_at)
VALUES (?, 'P', '', '', 'chat', '{}', '', '', 0)`, id); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM inbox`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows after migration = %d, %v", n, err)
	}
}

// openAtVersion creates a database with only the first v migrations applied,
// the way an older release left it.
func openAtVersion(t *testing.T, path string, v int) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	raw.SetMaxOpenConns(1)
	if _, err := raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < v; i++ {
		if _, err := raw.Exec(migrations[i]); err != nil {
			t.Fatalf("migration %d: %v", i+1, err)
		}
		if _, err := raw.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	return raw
}

// Upgrading a v1 store keeps every pairing and drops the trust levels:
// what a peer may do is now set per link.
func TestUpgradeKeepsPeersAndDropsTrust(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	raw := openAtVersion(t, path, 4)
	if _, err := raw.ExecContext(ctx, `INSERT INTO peers (machine_id, ik, alias, trust_in, prekey_json, relay_url, paused, paused_by_peer, paired_at)
VALUES ('m1', x'01', 'gpu-box', 3, '{}', 'https://relay.example.com', 0, 0, 1000)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer db.Close()
	p, err := db.GetPeer(ctx, "m1")
	if err != nil || p.Alias != "gpu-box" || p.RelayURL != "https://relay.example.com" {
		t.Fatalf("peer after upgrade = %+v, %v", p, err)
	}
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('peers') WHERE name = 'trust_in'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("trust_in column still there (%d, %v)", n, err)
	}
	if links, err := db.ListLinks(ctx, store.LinkFilter{}); err != nil || len(links) != 0 {
		t.Fatalf("an upgraded pairing carries links: %+v, %v", links, err)
	}
}

// v1 left tasks and queued envelopes without a link. Upgrading fails the
// unfinished v1 tasks locally (nothing is sent: the peer drops link-less
// traffic) and deletes queued link-scoped envelopes that carry no link_id.
func TestUpgradeFailsV1TasksAndDropsLinklessOutbox(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	raw := openAtVersion(t, path, 5)
	task := func(id, state, link string) {
		if _, err := raw.ExecContext(ctx, `INSERT INTO tasks (id, direction, peer, from_session, to_session, instructions, state,
claimed_by, notes_json, result, result_files_json, files_json, created_at, updated_at, expires_at, link_id)
VALUES (?, 'in', 'm1', '', '', 'x', ?, '', '[]', '', '[]', '[]', 1000, 1000, 5000, ?)`, id, state, link); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range []string{"sent", "awaiting_approval", "queued", "seen", "claimed", "running"} {
		task("v1-"+st, st, "")
	}
	task("v1-done", "done", "")
	task("v2-queued", "queued", "L1")
	outbox := func(id, envelope string) {
		if _, err := raw.ExecContext(ctx, `INSERT INTO outbox (id, to_machine, envelope, status, attempts, next_attempt, created_at)
VALUES (?, 'm1', ?, 'pending', 0, 0, 0)`, id, []byte(envelope)); err != nil {
			t.Fatal(err)
		}
	}
	outbox("chat-v1", `{"v":1,"id":"chat-v1","kind":"chat","body":{}}`)
	outbox("chat-empty", `{"v":1,"id":"chat-empty","kind":"chat","link_id":"","body":{}}`)
	outbox("task-v1", `{"v":1,"id":"task-v1","kind":"task.update","body":{}}`)
	outbox("offer-v1", `{"v":1,"id":"offer-v1","kind":"file.offer","body":{}}`)
	outbox("chat-v2", `{"v":1,"id":"chat-v2","kind":"chat","link_id":"L1","body":{}}`)
	outbox("closed", `{"v":1,"id":"closed","kind":"link.closed","body":{}}`)
	outbox("receipt", `{"v":1,"id":"receipt","kind":"control.delivered","body":{}}`)
	outbox("junk", `not json`)
	raw.Close()

	db, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer db.Close()
	for _, st := range []string{"sent", "awaiting_approval", "queued", "seen", "claimed", "running"} {
		got, err := db.GetTask(ctx, "v1-"+st)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != core.TaskFailed || !got.ExpiresAt.IsZero() || len(got.Notes) != 1 || got.Notes[0].Text != "no_link_after_upgrade" || got.Notes[0].At.IsZero() {
			t.Errorf("v1 task in %s after upgrade: %+v", st, got)
		}
	}
	if got, _ := db.GetTask(ctx, "v1-done"); got.State != core.TaskDone || len(got.Notes) != 0 {
		t.Errorf("finished v1 task changed: %+v", got)
	}
	if got, _ := db.GetTask(ctx, "v2-queued"); got.State != core.TaskQueued {
		t.Errorf("linked task changed: %+v", got)
	}
	var left []string
	rows, err := db.sql.QueryContext(ctx, `SELECT id FROM outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if strings.Join(left, ",") != "chat-v2,closed,junk,receipt" {
		t.Fatalf("outbox after upgrade: %v", left)
	}
}
