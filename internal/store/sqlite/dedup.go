package sqlite

import (
	"context"
	"time"
)

// SeenOrMark records id and reports whether it was already recorded.
// INSERT OR IGNORE is a single atomic statement: of two concurrent callers
// with the same id exactly one inserts (seen=false).
func (d *DB) SeenOrMark(ctx context.Context, id string, at time.Time) (bool, error) {
	res, err := d.sql.ExecContext(ctx, `INSERT OR IGNORE INTO dedup (id, seen_at) VALUES (?, ?)`, id, toMS(at))
	if err != nil {
		return false, err
	}
	n, err := affected(res)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

func (d *DB) PurgeDedupBefore(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM dedup WHERE seen_at < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}
