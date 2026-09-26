package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
