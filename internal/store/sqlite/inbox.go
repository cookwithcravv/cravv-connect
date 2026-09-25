package sqlite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

const inboxCols = `seq, msg_id, from_machine, from_session, to_session, kind, body, task_id, note, received_at, read_by_any`

func (d *DB) AddItem(ctx context.Context, it store.InboxItem) (int64, error) {
	body := []byte(it.Body)
	if body == nil {
		body = []byte("null")
	}
	res, err := d.sql.ExecContext(ctx, `
INSERT INTO inbox (msg_id, from_machine, from_session, to_session, kind, body, task_id, note, received_at, read_by_any)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.MsgID, string(it.From), it.FromSession, it.ToSession, string(it.Kind), body,
		it.TaskID, it.Note, toMS(it.ReceivedAt), boolInt(it.ReadByAny))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) ItemsFor(ctx context.Context, session string, after int64, limit int) ([]store.InboxItem, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+inboxCols+` FROM inbox
WHERE seq > ? AND (to_session = '' OR to_session = ?) ORDER BY seq LIMIT ?`,
		after, session, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.InboxItem
	for rows.Next() {
		it, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (d *DB) MarkRead(ctx context.Context, seqs []int64) error {
	if len(seqs) == 0 {
		return nil
	}
	args := make([]any, len(seqs))
	for i, s := range seqs {
		args[i] = s
	}
	_, err := d.sql.ExecContext(ctx,
		`UPDATE inbox SET read_by_any = 1 WHERE seq IN (`+placeholders(len(seqs))+`)`, args...)
	return err
}

func (d *DB) InitialCursor(ctx context.Context, since time.Time) (int64, error) {
	var c int64
	err := d.sql.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM inbox WHERE received_at < ? AND read_by_any = 1`,
		toMS(since)).Scan(&c)
	return c, err
}

func (d *DB) RedirectOrphans(ctx context.Context, session string, note string) (int, error) {
	if session == "" {
		return 0, nil
	}
	res, err := d.sql.ExecContext(ctx,
		`UPDATE inbox SET to_session = '', note = ? WHERE to_session = ?`, note, session)
	if err != nil {
		return 0, err
	}
	return affected(res)
}

func (d *DB) UnreadCount(ctx context.Context, session string, after int64) (int, map[core.MachineID]int, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT from_machine, COUNT(*) FROM inbox
WHERE seq > ? AND (to_session = '' OR to_session = ?) GROUP BY from_machine`, after, session)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	total := 0
	per := map[core.MachineID]int{}
	for rows.Next() {
		var (
			from string
			n    int
		)
		if err := rows.Scan(&from, &n); err != nil {
			return 0, nil, err
		}
		per[core.MachineID(from)] = n
		total += n
	}
	return total, per, rows.Err()
}

func (d *DB) PurgeInboxBefore(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM inbox WHERE received_at < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}

func scanInbox(s rowScanner) (store.InboxItem, error) {
	var (
		it         store.InboxItem
		from, kind string
		body       []byte
		received   int64
		read       int
	)
	if err := s.Scan(&it.Seq, &it.MsgID, &from, &it.FromSession, &it.ToSession, &kind, &body,
		&it.TaskID, &it.Note, &received, &read); err != nil {
		return store.InboxItem{}, notFound(err)
	}
	it.From = core.MachineID(from)
	it.Kind = core.Kind(kind)
	it.Body = json.RawMessage(body)
	it.ReceivedAt = fromMS(received)
	it.ReadByAny = read != 0
	return it, nil
}
