package sqlite

import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

const outboxCols = `id, to_machine, envelope, status, attempts, next_attempt, created_at`

func (d *DB) Enqueue(ctx context.Context, it store.OutboxItem) error {
	if it.Status == "" {
		it.Status = store.OutboxPending
	}
	_, err := d.sql.ExecContext(ctx, `INSERT INTO outbox (`+outboxCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		it.ID, string(it.To), it.Envelope, string(it.Status), it.Attempts, toMS(it.NextAttempt), toMS(it.CreatedAt))
	return err
}

func (d *DB) Due(ctx context.Context, now time.Time, limit int) ([]store.OutboxItem, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+outboxCols+` FROM outbox
WHERE status = ? AND next_attempt <= ? ORDER BY created_at, id LIMIT ?`,
		string(store.OutboxPending), toMS(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.OutboxItem
	for rows.Next() {
		it, err := scanOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (d *DB) Get(ctx context.Context, id string) (store.OutboxItem, error) {
	return scanOutbox(d.sql.QueryRowContext(ctx, `SELECT `+outboxCols+` FROM outbox WHERE id = ?`, id))
}

func (d *DB) SetStatus(ctx context.Context, id string, st store.OutboxStatus, attempts int, next time.Time) error {
	res, err := d.sql.ExecContext(ctx,
		`UPDATE outbox SET status = ?, attempts = ?, next_attempt = ? WHERE id = ?`,
		string(st), attempts, toMS(next), id)
	if err != nil {
		return err
	}
	n, err := affected(res)
	if err != nil {
		return err
	}
	if n == 0 {
		return core.ErrNotFound
	}
	return nil
}

func (d *DB) Delete(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := d.sql.ExecContext(ctx,
		`DELETE FROM outbox WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	return err
}

func (d *DB) HoldPeer(ctx context.Context, to core.MachineID) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE outbox SET status = ? WHERE to_machine = ? AND status IN (?, ?)`,
		string(store.OutboxHeld), string(to), string(store.OutboxPending), string(store.OutboxQueued))
	return err
}

func (d *DB) ReleasePeer(ctx context.Context, to core.MachineID, now time.Time) error {
	_, err := d.sql.ExecContext(ctx,
		`UPDATE outbox SET status = ?, next_attempt = ? WHERE to_machine = ? AND status = ?`,
		string(store.OutboxPending), toMS(now), string(to), string(store.OutboxHeld))
	return err
}

func (d *DB) DeleteOutboxForPeer(ctx context.Context, to core.MachineID) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM outbox WHERE to_machine = ?`, string(to))
	return err
}

func (d *DB) PurgeOutboxBefore(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM outbox WHERE created_at < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}

// CountOutbox reports items not yet delivered: pending counts both pending
// and queued (sent to the relay, awaiting control.delivered); held counts held.
func (d *DB) CountOutbox(ctx context.Context) (int, int, error) {
	var pending, held int
	err := d.sql.QueryRowContext(ctx, `SELECT
	COALESCE(SUM(CASE WHEN status IN (?, ?) THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN status = ? THEN 1 ELSE 0 END), 0)
FROM outbox`, string(store.OutboxPending), string(store.OutboxQueued), string(store.OutboxHeld)).Scan(&pending, &held)
	return pending, held, err
}

func scanOutbox(s rowScanner) (store.OutboxItem, error) {
	var (
		it              store.OutboxItem
		to, status      string
		next, createdAt int64
	)
	if err := s.Scan(&it.ID, &to, &it.Envelope, &status, &it.Attempts, &next, &createdAt); err != nil {
		return store.OutboxItem{}, notFound(err)
	}
	it.To = core.MachineID(to)
	it.Status = store.OutboxStatus(status)
	it.NextAttempt = fromMS(next)
	it.CreatedAt = fromMS(createdAt)
	return it, nil
}
