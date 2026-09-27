package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

const inboxCols = `seq, msg_id, from_machine, from_session, to_session, kind, body, task_id, note, received_at, link_id`

// AddItem inserts an item. A chat whose (msg_id, to_session) is already
// stored is not inserted again; the existing item's seq is returned.
func (d *DB) AddItem(ctx context.Context, it store.InboxItem) (int64, error) {
	body := []byte(it.Body)
	if body == nil {
		body = []byte("null")
	}
	var seq int64
	err := inTx(ctx, d.sql, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
INSERT INTO inbox (msg_id, from_machine, from_session, to_session, kind, body, task_id, note, received_at, link_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			it.MsgID, string(it.From), it.FromSession, it.ToSession, string(it.Kind), body,
			it.TaskID, it.Note, toMS(it.ReceivedAt), it.LinkID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 1 {
			seq, err = res.LastInsertId()
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT seq FROM inbox WHERE msg_id = ? AND to_session = ? AND kind = ?`,
			it.MsgID, it.ToSession, string(it.Kind)).Scan(&seq)
	})
	return seq, err
}

func (d *DB) PurgeInboxBefore(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM inbox WHERE received_at < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}

// HasInboxMsg reports whether any inbox item carries msgID.
func (d *DB) HasInboxMsg(ctx context.Context, msgID string) (bool, error) {
	var n int
	err := d.sql.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM inbox WHERE msg_id = ?)`, msgID).Scan(&n)
	return n == 1, err
}

// SessionItems returns the items addressed to exactly this shared session.
func (d *DB) SessionItems(ctx context.Context, session string, after int64, limit int) ([]store.InboxItem, error) {
	if session == "" {
		return nil, nil
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT `+inboxCols+` FROM inbox
WHERE to_session = ? AND seq > ? ORDER BY seq LIMIT ?`, session, after, limit)
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

// SessionUnread counts the session's items after the cursor, per sender.
func (d *DB) SessionUnread(ctx context.Context, session string, after int64) (int, map[core.MachineID]int, error) {
	per := map[core.MachineID]int{}
	if session == "" {
		return 0, per, nil
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT from_machine, COUNT(*) FROM inbox
WHERE to_session = ? AND seq > ? GROUP BY from_machine`, session, after)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	total := 0
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

// SessionUnreadGroups counts the session's items after the cursor per
// sender, link and kind, oldest group first.
func (d *DB) SessionUnreadGroups(ctx context.Context, session string, after int64) ([]store.UnreadGroup, error) {
	if session == "" {
		return nil, nil
	}
	rows, err := d.sql.QueryContext(ctx, `SELECT from_machine, link_id, kind, COUNT(*), MAX(seq) FROM inbox
WHERE to_session = ? AND seq > ? GROUP BY from_machine, link_id, kind ORDER BY MIN(seq)`, session, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.UnreadGroup
	for rows.Next() {
		var (
			g          store.UnreadGroup
			from, kind string
		)
		if err := rows.Scan(&from, &g.LinkID, &kind, &g.Count, &g.MaxSeq); err != nil {
			return nil, err
		}
		g.Peer, g.Kind = core.MachineID(from), core.Kind(kind)
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteSessionItems drops the session's unread items from one link.
func (d *DB) DeleteSessionItems(ctx context.Context, session, linkID string, after int64) (int, error) {
	if session == "" || linkID == "" {
		return 0, nil
	}
	res, err := d.sql.ExecContext(ctx, `DELETE FROM inbox WHERE to_session = ? AND link_id = ? AND seq > ?`,
		session, linkID, after)
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
	)
	if err := s.Scan(&it.Seq, &it.MsgID, &from, &it.FromSession, &it.ToSession, &kind, &body,
		&it.TaskID, &it.Note, &received, &it.LinkID); err != nil {
		return store.InboxItem{}, notFound(err)
	}
	it.From = core.MachineID(from)
	it.Kind = core.Kind(kind)
	it.Body = json.RawMessage(body)
	it.ReceivedAt = fromMS(received)
	return it, nil
}
