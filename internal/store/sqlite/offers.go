package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

const offerCols = `id, peer, label, folder, real_folder, agent, permission, run_mode, max_concurrent,
	idle_timeout_ms, max_turns, run_timeout_ms, runs_per_hour, runs_per_day, created_at, updated_at`

// PutOffer upserts an offer by ID. The unique index on (peer, label) turns
// a clash with another offer to the same machine into store.ErrOfferLabelTaken.
func (d *DB) PutOffer(ctx context.Context, o store.Offer) error {
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO offers (`+offerCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET peer = excluded.peer, label = excluded.label, folder = excluded.folder,
	real_folder = excluded.real_folder, agent = excluded.agent, permission = excluded.permission,
	run_mode = excluded.run_mode, max_concurrent = excluded.max_concurrent,
	idle_timeout_ms = excluded.idle_timeout_ms, max_turns = excluded.max_turns,
	run_timeout_ms = excluded.run_timeout_ms, runs_per_hour = excluded.runs_per_hour,
	runs_per_day = excluded.runs_per_day, created_at = excluded.created_at, updated_at = excluded.updated_at`,
		o.ID, string(o.Peer), o.Label, o.Folder, o.RealFolder, o.Agent, string(o.Permission), string(o.RunMode),
		o.MaxConcurrent, o.IdleTimeout.Milliseconds(), o.MaxTurnsPerRun, o.RunTimeout.Milliseconds(),
		o.RunsPerHour, o.RunsPerDay, toMS(o.CreatedAt), toMS(o.UpdatedAt))
	if err != nil && strings.Contains(err.Error(), "offers.peer") {
		return store.ErrOfferLabelTaken
	}
	return err
}

func (d *DB) GetOffer(ctx context.Context, id string) (store.Offer, error) {
	return scanOffer(d.sql.QueryRowContext(ctx, `SELECT `+offerCols+` FROM offers WHERE id = ?`, id))
}

func (d *DB) ListOffers(ctx context.Context, peer core.MachineID) ([]store.Offer, error) {
	q := `SELECT ` + offerCols + ` FROM offers`
	var args []any
	if peer != "" {
		q += ` WHERE peer = ?`
		args = append(args, string(peer))
	}
	rows, err := d.sql.QueryContext(ctx, q+` ORDER BY label, peer`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Offer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (d *DB) DeleteOffer(ctx context.Context, id string) error {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM offers WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := affected(res); err != nil || n == 0 {
		if err == nil {
			err = core.ErrNotFound
		}
		return err
	}
	return nil
}

func scanOffer(s rowScanner) (store.Offer, error) {
	var (
		o                    store.Offer
		peer, perm, mode     string
		idleMS, runMS        int64
		createdAt, updatedAt int64
	)
	if err := s.Scan(&o.ID, &peer, &o.Label, &o.Folder, &o.RealFolder, &o.Agent, &perm, &mode, &o.MaxConcurrent,
		&idleMS, &o.MaxTurnsPerRun, &runMS, &o.RunsPerHour, &o.RunsPerDay, &createdAt, &updatedAt); err != nil {
		return store.Offer{}, notFound(err)
	}
	o.Peer = core.MachineID(peer)
	o.Permission = core.Permission(perm)
	o.RunMode = core.RunMode(mode)
	o.IdleTimeout = time.Duration(idleMS) * time.Millisecond
	o.RunTimeout = time.Duration(runMS) * time.Millisecond
	o.CreatedAt = fromMS(createdAt)
	o.UpdatedAt = fromMS(updatedAt)
	return o, nil
}

const managedCols = `session_id, offer_id, peer, link_id, agent_session, started, last_active, created_at`

func (d *DB) PutManaged(ctx context.Context, m store.ManagedSession) error {
	_, err := d.sql.ExecContext(ctx, `
INSERT INTO managed_sessions (`+managedCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET offer_id = excluded.offer_id, peer = excluded.peer,
	link_id = excluded.link_id, agent_session = excluded.agent_session, started = excluded.started,
	last_active = excluded.last_active, created_at = excluded.created_at`,
		m.SessionID, m.OfferID, string(m.Peer), m.LinkID, m.AgentSession, boolInt(m.Started),
		toMS(m.LastActive), toMS(m.CreatedAt))
	return err
}

func (d *DB) GetManaged(ctx context.Context, sessionID string) (store.ManagedSession, error) {
	return scanManaged(d.sql.QueryRowContext(ctx, `SELECT `+managedCols+` FROM managed_sessions WHERE session_id = ?`, sessionID))
}

func (d *DB) ListManaged(ctx context.Context) ([]store.ManagedSession, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+managedCols+` FROM managed_sessions ORDER BY created_at, session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ManagedSession
	for rows.Next() {
		m, err := scanManaged(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanManaged(s rowScanner) (store.ManagedSession, error) {
	var (
		m                     store.ManagedSession
		peer                  string
		started               int
		lastActive, createdAt int64
	)
	if err := s.Scan(&m.SessionID, &m.OfferID, &peer, &m.LinkID, &m.AgentSession, &started, &lastActive, &createdAt); err != nil {
		return store.ManagedSession{}, notFound(err)
	}
	m.Peer = core.MachineID(peer)
	m.Started = started != 0
	m.LastActive = fromMS(lastActive)
	m.CreatedAt = fromMS(createdAt)
	return m, nil
}

func (d *DB) AddRun(ctx context.Context, r store.ManagedRun) error {
	_, err := d.sql.ExecContext(ctx, `INSERT INTO managed_runs (id, session_id, peer, link_id, started_at) VALUES (?, ?, ?, ?, ?)`,
		r.ID, r.SessionID, string(r.Peer), r.LinkID, toMS(r.StartedAt))
	return err
}

// AddRunCapped counts and inserts in one BEGIN IMMEDIATE transaction, so
// concurrent starts cannot both see room under a cap.
func (d *DB) AddRunCapped(ctx context.Context, r store.ManagedRun, caps store.RunCaps) (store.RunCap, error) {
	capped := store.RunCapNone
	err := inTx(ctx, d.sql, func(tx *sql.Tx) error {
		var link, peer int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_runs WHERE link_id = ? AND started_at >= ?`,
			r.LinkID, toMS(caps.LinkSince)).Scan(&link); err != nil {
			return err
		}
		if link >= caps.PerLink {
			capped = store.RunCapLink
			return nil
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM managed_runs WHERE peer = ? AND started_at >= ?`,
			string(r.Peer), toMS(caps.PeerSince)).Scan(&peer); err != nil {
			return err
		}
		if peer >= caps.PerPeer {
			capped = store.RunCapPeer
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO managed_runs (id, session_id, peer, link_id, started_at) VALUES (?, ?, ?, ?, ?)`,
			r.ID, r.SessionID, string(r.Peer), r.LinkID, toMS(r.StartedAt))
		return err
	})
	if err != nil {
		return store.RunCapNone, err
	}
	return capped, nil
}

func (d *DB) CountRuns(ctx context.Context, f store.RunFilter) (int, error) {
	var (
		where []string
		args  []any
	)
	if f.Peer != "" {
		where = append(where, "peer = ?")
		args = append(args, string(f.Peer))
	}
	if f.LinkID != "" {
		where = append(where, "link_id = ?")
		args = append(args, f.LinkID)
	}
	if !f.Since.IsZero() {
		where = append(where, "started_at >= ?")
		args = append(args, toMS(f.Since))
	}
	q := `SELECT COUNT(*) FROM managed_runs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	var n int
	err := d.sql.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

func (d *DB) PurgeRunsBefore(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM managed_runs WHERE started_at < ?`, toMS(t))
	if err != nil {
		return 0, err
	}
	return affected(res)
}
