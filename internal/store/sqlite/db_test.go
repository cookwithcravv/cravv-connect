package sqlite

import (
	"context"
	"os"
	"path/filepath"
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
