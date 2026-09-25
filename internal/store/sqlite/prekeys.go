package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/cravv/cravv-connect/internal/store"
)

func (d *DB) PutPrekey(ctx context.Context, p store.PrekeyRecord) error {
	var sup any
	if p.SupersededAt != nil {
		sup = toMS(*p.SupersededAt)
	}
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO prekeys (id, priv, created_at, superseded_at) VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET priv = excluded.priv, created_at = excluded.created_at,
	superseded_at = excluded.superseded_at`,
		p.ID, p.Priv, toMS(p.CreatedAt), sup)
	return err
}

func (d *DB) CurrentPrekey(ctx context.Context) (store.PrekeyRecord, error) {
	return scanPrekey(d.sql.QueryRowContext(ctx, `
SELECT id, priv, created_at, superseded_at FROM prekeys
WHERE superseded_at IS NULL ORDER BY created_at DESC, id DESC LIMIT 1`))
}

func (d *DB) GetPrekey(ctx context.Context, id string) (store.PrekeyRecord, error) {
	return scanPrekey(d.sql.QueryRowContext(ctx,
		`SELECT id, priv, created_at, superseded_at FROM prekeys WHERE id = ?`, id))
}

func (d *DB) SupersedeAllExcept(ctx context.Context, id string, at time.Time) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE prekeys SET superseded_at = ? WHERE id <> ? AND superseded_at IS NULL`, toMS(at), id)
	return err
}

func (d *DB) DeleteSupersededBefore(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx,
		`DELETE FROM prekeys WHERE superseded_at IS NOT NULL AND superseded_at < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}

func scanPrekey(s rowScanner) (store.PrekeyRecord, error) {
	var (
		p       store.PrekeyRecord
		created int64
		sup     sql.NullInt64
	)
	if err := s.Scan(&p.ID, &p.Priv, &created, &sup); err != nil {
		return store.PrekeyRecord{}, notFound(err)
	}
	p.CreatedAt = fromMS(created)
	if sup.Valid {
		t := fromMS(sup.Int64)
		p.SupersededAt = &t
	}
	return p, nil
}
