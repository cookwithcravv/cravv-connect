package sqlite

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

const peerCols = `machine_id, ik, alias, trust_in, prekey_json, relay_url, paused, paused_by_peer, paired_at`

func (d *DB) PutPeer(ctx context.Context, p store.Peer) error {
	pk, err := json.Marshal(p.Prekey)
	if err != nil {
		return err
	}
	return inTx(ctx, d.sql, func(tx *sql.Tx) error {
		var other string
		err := tx.QueryRowContext(ctx,
			`SELECT machine_id FROM peers WHERE alias = ? AND machine_id <> ?`,
			p.Alias, string(p.MachineID)).Scan(&other)
		switch {
		case err == nil:
			return store.ErrAliasTaken
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO peers (`+peerCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(machine_id) DO UPDATE SET
	ik = excluded.ik, alias = excluded.alias, trust_in = excluded.trust_in,
	prekey_json = excluded.prekey_json, relay_url = excluded.relay_url,
	paused = excluded.paused, paused_by_peer = excluded.paused_by_peer,
	paired_at = excluded.paired_at`,
			string(p.MachineID), []byte(p.IK), p.Alias, int(p.TrustIn), string(pk), p.RelayURL,
			boolInt(p.Paused), boolInt(p.PausedByPeer), toMS(p.PairedAt))
		return err
	})
}

func (d *DB) GetPeer(ctx context.Context, id core.MachineID) (store.Peer, error) {
	row := d.sql.QueryRowContext(ctx, `SELECT `+peerCols+` FROM peers WHERE machine_id = ?`, string(id))
	return scanPeer(row)
}

func (d *DB) GetPeerByAlias(ctx context.Context, alias string) (store.Peer, error) {
	row := d.sql.QueryRowContext(ctx, `SELECT `+peerCols+` FROM peers WHERE alias = ?`, alias)
	return scanPeer(row)
}

func (d *DB) ListPeers(ctx context.Context) ([]store.Peer, error) {
	rows, err := d.sql.QueryContext(ctx, `SELECT `+peerCols+` FROM peers ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Peer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) DeletePeer(ctx context.Context, id core.MachineID) error {
	_, err := d.sql.ExecContext(ctx, `DELETE FROM peers WHERE machine_id = ?`, string(id))
	return err
}

func scanPeer(s rowScanner) (store.Peer, error) {
	var (
		p                  store.Peer
		id, pk             string
		ik                 []byte
		trust, paused, pbp int
		pairedAt           int64
	)
	if err := s.Scan(&id, &ik, &p.Alias, &trust, &pk, &p.RelayURL, &paused, &pbp, &pairedAt); err != nil {
		return store.Peer{}, notFound(err)
	}
	if err := json.Unmarshal([]byte(pk), &p.Prekey); err != nil {
		return store.Peer{}, err
	}
	p.MachineID = core.MachineID(id)
	p.IK = ed25519.PublicKey(ik)
	p.TrustIn = core.TrustLevel(trust)
	p.Paused = paused != 0
	p.PausedByPeer = pbp != 0
	p.PairedAt = fromMS(pairedAt)
	return p, nil
}
