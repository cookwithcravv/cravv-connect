// Package sqlite implements the store repositories on SQLite using the
// pure-Go modernc.org/sqlite driver (no cgo).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// DB owns the SQLite handle and implements store.Store. The methods of
// each repository interface live in the file named after it.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the database at path with mode 0600,
// enables WAL, a 5 s busy timeout and foreign keys, and applies migrations.
// The pool is limited to one connection: every transaction is serialized
// inside the process, which makes read-check-write transactions atomic and
// avoids SQLITE_BUSY between our own connections.
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolve path: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: create db file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("store: create db file: %w", err)
	}
	if err := os.Chmod(abs, 0o600); err != nil {
		return nil, fmt.Errorf("store: chmod db file: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}).String()
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	sdb.SetMaxOpenConns(1)
	sdb.SetMaxIdleConns(1)
	sdb.SetConnMaxLifetime(0)
	if err := migrate(context.Background(), sdb); err != nil {
		_ = sdb.Close()
		return nil, err
	}
	return &DB{sql: sdb}, nil
}

// Close closes the underlying database.
func (d *DB) Close() error { return d.sql.Close() }

// toMS stores a time as unix milliseconds; the zero time is stored as 0.
func toMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// fromMS is the inverse of toMS; 0 reads back as the zero time.
func fromMS(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// notFound maps sql.ErrNoRows to core.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return core.ErrNotFound
	}
	return err
}

// inTx runs fn inside a transaction, committing on success.
func inTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func affected(res sql.Result) (int, error) {
	n, err := res.RowsAffected()
	return int(n), err
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// execer is satisfied by *sql.DB and *sql.Tx.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// placeholders returns "?, ?, ?" with n marks (n >= 1).
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// DB implements every repository interface.
var _ store.Store = (*DB)(nil)
