package sqlite

import (
	"context"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

const sessionCols = `name, agent, project_dir, cursor, last_seen, connected`

func (d *DB) PutSession(ctx context.Context, s store.SessionRecord) error {
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO sessions (`+sessionCols+`) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(name) DO UPDATE SET agent = excluded.agent, project_dir = excluded.project_dir,
	cursor = excluded.cursor, last_seen = excluded.last_seen, connected = excluded.connected`,
		s.Name, s.Agent, s.ProjectDir, s.Cursor, toMS(s.LastSeen), boolInt(s.Connected))
	return err
}

func (d *DB) GetSession(ctx context.Context, name string) (store.SessionRecord, error) {
	return scanSession(d.sql.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM sessions WHERE name = ?`, name))
}

func (d *DB) ListSessions(ctx context.Context) ([]store.SessionRecord, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+sessionCols+` FROM sessions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.SessionRecord
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) SetCursor(ctx context.Context, name string, cursor int64) error {
	res, err := d.sql.ExecContext(ctx, `UPDATE sessions SET cursor = ? WHERE name = ?`, cursor, name)
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

func (d *DB) DeleteSession(ctx context.Context, name string) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM sessions WHERE name = ?`, name)
	return err
}

func scanSession(s rowScanner) (store.SessionRecord, error) {
	var (
		rec       store.SessionRecord
		lastSeen  int64
		connected int
	)
	if err := s.Scan(&rec.Name, &rec.Agent, &rec.ProjectDir, &rec.Cursor, &lastSeen, &connected); err != nil {
		return store.SessionRecord{}, notFound(err)
	}
	rec.LastSeen = fromMS(lastSeen)
	rec.Connected = connected != 0
	return rec, nil
}
