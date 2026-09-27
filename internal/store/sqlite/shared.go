package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

const sharedCols = `id, name, purpose, kind, agent, project_dir, visibility_json, state,
	reattach_hash, wake_hash, cursor, created_at, state_since`

// PutShared upserts a shared session. The partial unique index on name
// (state != 'closed') turns a clash with another open or away session into
// store.ErrNameTaken.
func (d *DB) PutShared(ctx context.Context, s store.SharedSession) error {
	vis, err := json.Marshal(s.Visibility)
	if err != nil {
		return err
	}
	_, err = d.sql.ExecContext(ctx, `
INSERT INTO shared_sessions (`+sharedCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, purpose = excluded.purpose, kind = excluded.kind,
	agent = excluded.agent, project_dir = excluded.project_dir, visibility_json = excluded.visibility_json,
	state = excluded.state, reattach_hash = excluded.reattach_hash, wake_hash = excluded.wake_hash,
	cursor = excluded.cursor, created_at = excluded.created_at, state_since = excluded.state_since`,
		s.ID, s.Name, s.Purpose, string(s.Kind), s.Agent, s.ProjectDir, string(vis), string(s.State),
		s.ReattachHash, s.WakeHash, s.Cursor, toMS(s.CreatedAt), toMS(s.StateSince))
	if err != nil && strings.Contains(err.Error(), "shared_sessions.name") {
		return store.ErrNameTaken
	}
	return err
}

func (d *DB) GetShared(ctx context.Context, id string) (store.SharedSession, error) {
	return scanShared(d.sql.QueryRowContext(ctx, `SELECT `+sharedCols+` FROM shared_sessions WHERE id = ?`, id))
}

func (d *DB) SharedByWakeHash(ctx context.Context, hash string) (store.SharedSession, error) {
	return d.sharedByHash(ctx, "wake_hash", hash)
}

func (d *DB) SharedByReattachHash(ctx context.Context, hash string) (store.SharedSession, error) {
	return d.sharedByHash(ctx, "reattach_hash", hash)
}

// sharedByHash looks a live session up by one of the two token hash columns.
// col is one of two constants, never input.
func (d *DB) sharedByHash(ctx context.Context, col, hash string) (store.SharedSession, error) {
	if hash == "" {
		return store.SharedSession{}, core.ErrNotFound
	}
	return scanShared(d.sql.QueryRowContext(ctx, `SELECT `+sharedCols+` FROM shared_sessions
WHERE `+col+` = ? AND state != 'closed'`, hash))
}

func (d *DB) ListShared(ctx context.Context, states ...core.SessionState) ([]store.SharedSession, error) {
	q := `SELECT ` + sharedCols + ` FROM shared_sessions`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (` + placeholders(len(states)) + `)`
		for _, s := range states {
			args = append(args, string(s))
		}
	}
	q += ` ORDER BY created_at, id`
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.SharedSession
	for rows.Next() {
		s, err := scanShared(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) SetSharedCursor(ctx context.Context, id string, cursor int64) error {
	res, err := d.sql.ExecContext(ctx, `UPDATE shared_sessions SET cursor = ? WHERE id = ?`, cursor, id)
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

func (d *DB) PurgeClosedShared(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM shared_sessions WHERE state = 'closed' AND state_since < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}

func scanShared(s rowScanner) (store.SharedSession, error) {
	var (
		rec                   store.SharedSession
		kind, vis, state      string
		createdAt, stateSince int64
	)
	if err := s.Scan(&rec.ID, &rec.Name, &rec.Purpose, &kind, &rec.Agent, &rec.ProjectDir, &vis, &state,
		&rec.ReattachHash, &rec.WakeHash, &rec.Cursor, &createdAt, &stateSince); err != nil {
		return store.SharedSession{}, notFound(err)
	}
	if err := json.Unmarshal([]byte(vis), &rec.Visibility); err != nil {
		return store.SharedSession{}, err
	}
	rec.Kind = core.SessionKind(kind)
	rec.State = core.SessionState(state)
	rec.CreatedAt = fromMS(createdAt)
	rec.StateSince = fromMS(stateSince)
	return rec, nil
}
