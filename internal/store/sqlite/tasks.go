package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

const taskCols = `id, direction, peer, from_session, to_session, instructions, state, claimed_by,
	notes_json, result, result_files_json, files_json, created_at, updated_at, expires_at, link_id`

func (d *DB) PutTask(ctx context.Context, t store.Task) error {
	return upsertTask(ctx, d.sql, t)
}

func upsertTask(ctx context.Context, ex execer, t store.Task) error {
	notes, err := json.Marshal(nonNilNotes(t.Notes))
	if err != nil {
		return err
	}
	rfiles, err := json.Marshal(nonNilRefs(t.ResultFiles))
	if err != nil {
		return err
	}
	files, err := json.Marshal(nonNilRefs(t.Files))
	if err != nil {
		return err
	}
	_, err = ex.ExecContext(ctx, `
INSERT INTO tasks (`+taskCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET direction = excluded.direction, peer = excluded.peer,
	from_session = excluded.from_session, to_session = excluded.to_session,
	instructions = excluded.instructions, state = excluded.state, claimed_by = excluded.claimed_by,
	notes_json = excluded.notes_json, result = excluded.result,
	result_files_json = excluded.result_files_json, files_json = excluded.files_json,
	created_at = excluded.created_at, updated_at = excluded.updated_at, expires_at = excluded.expires_at,
	link_id = excluded.link_id`,
		t.ID, string(t.Direction), string(t.Peer), t.FromSession, t.ToSession, t.Instructions,
		string(t.State), t.ClaimedBy, string(notes), t.Result, string(rfiles), string(files),
		toMS(t.CreatedAt), toMS(t.UpdatedAt), toMS(t.ExpiresAt), t.LinkID)
	return err
}

func (d *DB) GetTask(ctx context.Context, id string) (store.Task, error) {
	return scanTask(d.sql.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
}

// Transition reads the task, checks its state is in from, applies mutate
// and writes it back, all inside one transaction. Because the pool holds a
// single connection, concurrent Transitions are serialized: two sessions
// claiming the same queued task cannot both succeed.
// mutate runs inside the transaction and MUST NOT call d (see store.TaskStore).
func (d *DB) Transition(ctx context.Context, id string, from []core.TaskState, mutate func(*store.Task) error) (store.Task, error) {
	var out store.Task
	err := inTx(ctx, d.sql, func(tx *sql.Tx) error {
		t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if !slices.Contains(from, t.State) {
			return fmt.Errorf("task %s is %s: %w", id, t.State, core.ErrBadTransition)
		}
		if err := mutate(&t); err != nil {
			return err
		}
		t.ID = id // the mutate func may not rename the row
		if err := upsertTask(ctx, tx, t); err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return store.Task{}, err
	}
	return out, nil
}

func (d *DB) ListTasks(ctx context.Context, f store.TaskFilter) ([]store.Task, error) {
	var (
		where []string
		args  []any
	)
	if f.Direction != "" {
		where = append(where, "direction = ?")
		args = append(args, string(f.Direction))
	}
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholders(len(f.States))+")")
		for _, s := range f.States {
			args = append(args, string(s))
		}
	}
	if f.ClaimedBy != "" {
		where = append(where, "claimed_by = ?")
		args = append(args, f.ClaimedBy)
	}
	if f.Peer != "" {
		where = append(where, "peer = ?")
		args = append(args, string(f.Peer))
	}
	if f.LinkID != "" {
		where = append(where, "link_id = ?")
		args = append(args, f.LinkID)
	}
	if !f.ExpiredBefore.IsZero() {
		where = append(where, "expires_at <> 0 AND expires_at <= ?")
		args = append(args, toMS(f.ExpiredBefore))
	}
	q := `SELECT ` + taskCols + ` FROM tasks`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY created_at, id"
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanTask(s rowScanner) (store.Task, error) {
	var (
		t                               store.Task
		dir, peer, state                string
		notes, rfiles, files            string
		createdAt, updatedAt, expiresAt int64
	)
	if err := s.Scan(&t.ID, &dir, &peer, &t.FromSession, &t.ToSession, &t.Instructions, &state,
		&t.ClaimedBy, &notes, &t.Result, &rfiles, &files, &createdAt, &updatedAt, &expiresAt, &t.LinkID); err != nil {
		return store.Task{}, notFound(err)
	}
	if err := json.Unmarshal([]byte(notes), &t.Notes); err != nil {
		return store.Task{}, err
	}
	if err := json.Unmarshal([]byte(rfiles), &t.ResultFiles); err != nil {
		return store.Task{}, err
	}
	if err := json.Unmarshal([]byte(files), &t.Files); err != nil {
		return store.Task{}, err
	}
	if len(t.Notes) == 0 {
		t.Notes = nil
	}
	if len(t.ResultFiles) == 0 {
		t.ResultFiles = nil
	}
	if len(t.Files) == 0 {
		t.Files = nil
	}
	t.Direction = store.TaskDirection(dir)
	t.Peer = core.MachineID(peer)
	t.State = core.TaskState(state)
	t.CreatedAt = fromMS(createdAt)
	t.UpdatedAt = fromMS(updatedAt)
	t.ExpiresAt = fromMS(expiresAt)
	return t, nil
}

func nonNilNotes(n []store.TaskNote) []store.TaskNote {
	if n == nil {
		return []store.TaskNote{}
	}
	return n
}

func nonNilRefs(r []core.FileRef) []core.FileRef {
	if r == nil {
		return []core.FileRef{}
	}
	return r
}
