package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

const linkCols = `num, peer, link_id, direction, session_id, remote_session, remote_name, remote_purpose,
	permission_in, permission_out, proposed, note, state, remote_away, reason, created_at, updated_at, expires_at`

// InsertLink stores a new link and returns it with Num assigned.
func (d *DB) InsertLink(ctx context.Context, l store.Link) (store.Link, error) {
	res, err := d.sql.ExecContext(ctx, `
INSERT INTO links (peer, link_id, direction, session_id, remote_session, remote_name, remote_purpose,
	permission_in, permission_out, proposed, note, state, remote_away, reason, created_at, updated_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(l.Peer), l.ID, string(l.Direction), l.Session, l.RemoteSession, l.RemoteName, l.RemotePurpose,
		string(l.PermissionIn), string(l.PermissionOut), string(l.Proposed), l.Note, string(l.State),
		boolInt(l.RemoteAway), l.Reason, toMS(l.CreatedAt), toMS(l.UpdatedAt), toMS(l.ExpiresAt))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return store.Link{}, store.ErrLinkExists
		}
		return store.Link{}, err
	}
	if l.Num, err = res.LastInsertId(); err != nil {
		return store.Link{}, err
	}
	return l, nil
}

func (d *DB) GetLink(ctx context.Context, peer core.MachineID, id string) (store.Link, error) {
	return scanLink(d.sql.QueryRowContext(ctx, `SELECT `+linkCols+` FROM links WHERE peer = ? AND link_id = ?`, string(peer), id))
}

func (d *DB) GetLinkByNum(ctx context.Context, num int64) (store.Link, error) {
	return scanLink(d.sql.QueryRowContext(ctx, `SELECT `+linkCols+` FROM links WHERE num = ?`, num))
}

func (d *DB) ListLinks(ctx context.Context, f store.LinkFilter) ([]store.Link, error) {
	var (
		where []string
		args  []any
	)
	if f.Peer != "" {
		where = append(where, "peer = ?")
		args = append(args, string(f.Peer))
	}
	if f.Session != "" {
		where = append(where, "session_id = ?")
		args = append(args, f.Session)
	}
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholders(len(f.States))+")")
		for _, s := range f.States {
			args = append(args, string(s))
		}
	}
	if f.Direction != "" {
		where = append(where, "direction = ?")
		args = append(args, string(f.Direction))
	}
	if !f.ExpiredBefore.IsZero() {
		where = append(where, "expires_at <> 0 AND expires_at <= ?")
		args = append(args, toMS(f.ExpiredBefore))
	}
	q := `SELECT ` + linkCols + ` FROM links`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY num"
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UpdateLink reads, mutates and writes the link in one transaction.
// mutate runs inside the transaction and MUST NOT call d (see store.LinkStore).
func (d *DB) UpdateLink(ctx context.Context, peer core.MachineID, id string, mutate func(*store.Link) error) (store.Link, error) {
	var out store.Link
	err := inTx(ctx, d.sql, func(tx *sql.Tx) error {
		l, err := scanLink(tx.QueryRowContext(ctx, `SELECT `+linkCols+` FROM links WHERE peer = ? AND link_id = ?`, string(peer), id))
		if err != nil {
			return err
		}
		num := l.Num
		if err := mutate(&l); err != nil {
			return err
		}
		// The key and the local handle never change.
		_, err = tx.ExecContext(ctx, `
UPDATE links SET direction = ?, session_id = ?, remote_session = ?, remote_name = ?, remote_purpose = ?,
	permission_in = ?, permission_out = ?, proposed = ?, note = ?, state = ?, remote_away = ?, reason = ?,
	created_at = ?, updated_at = ?, expires_at = ?
WHERE peer = ? AND link_id = ?`,
			string(l.Direction), l.Session, l.RemoteSession, l.RemoteName, l.RemotePurpose,
			string(l.PermissionIn), string(l.PermissionOut), string(l.Proposed), l.Note, string(l.State),
			boolInt(l.RemoteAway), l.Reason, toMS(l.CreatedAt), toMS(l.UpdatedAt), toMS(l.ExpiresAt),
			string(peer), id)
		if err != nil {
			return err
		}
		l.Num, l.Peer, l.ID = num, peer, id
		out = l
		return nil
	})
	if err != nil {
		return store.Link{}, err
	}
	return out, nil
}

func (d *DB) PurgeClosedLinks(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM links WHERE state = 'closed' AND updated_at < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}

func scanLink(s rowScanner) (store.Link, error) {
	var (
		l                                     store.Link
		peer, dir, pin, pout, proposed, state string
		away                                  int
		createdAt, updatedAt, expiresAt       int64
	)
	if err := s.Scan(&l.Num, &peer, &l.ID, &dir, &l.Session, &l.RemoteSession, &l.RemoteName, &l.RemotePurpose,
		&pin, &pout, &proposed, &l.Note, &state, &away, &l.Reason, &createdAt, &updatedAt, &expiresAt); err != nil {
		return store.Link{}, notFound(err)
	}
	l.Peer = core.MachineID(peer)
	l.Direction = store.LinkDirection(dir)
	l.PermissionIn = core.Permission(pin)
	l.PermissionOut = core.Permission(pout)
	l.Proposed = core.Permission(proposed)
	l.State = store.LinkState(state)
	l.RemoteAway = away != 0
	l.CreatedAt = fromMS(createdAt)
	l.UpdatedAt = fromMS(updatedAt)
	l.ExpiresAt = fromMS(expiresAt)
	return l, nil
}
