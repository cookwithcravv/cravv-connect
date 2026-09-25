package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

const fileCols = `file_id, direction, peer, msg_id, blob_id, name, size, chunks, sha256, key,
	task_id, state, local_path, next_chunk, attempts, reason, created_at`

func (d *DB) PutFile(ctx context.Context, f store.FileRecord) error {
	return upsertFile(ctx, d.sql, f)
}

func upsertFile(ctx context.Context, ex execer, f store.FileRecord) error {
	_, err := ex.ExecContext(ctx, `
INSERT INTO files (`+fileCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(file_id) DO UPDATE SET direction = excluded.direction, peer = excluded.peer,
	msg_id = excluded.msg_id, blob_id = excluded.blob_id, name = excluded.name, size = excluded.size,
	chunks = excluded.chunks, sha256 = excluded.sha256, key = excluded.key, task_id = excluded.task_id,
	state = excluded.state, local_path = excluded.local_path, next_chunk = excluded.next_chunk,
	attempts = excluded.attempts, reason = excluded.reason, created_at = excluded.created_at`,
		f.FileID, string(f.Direction), string(f.Peer), f.MsgID, f.BlobID, f.Name, f.Size, int64(f.Chunks),
		f.SHA256, f.Key, f.TaskID, string(f.State), f.LocalPath, int64(f.NextChunk), f.Attempts, f.Reason,
		toMS(f.CreatedAt))
	return err
}

func (d *DB) GetFile(ctx context.Context, id string) (store.FileRecord, error) {
	return scanFile(d.sql.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE file_id = ?`, id))
}

// ListFiles returns files in any of states (all files when states is empty), oldest first.
func (d *DB) ListFiles(ctx context.Context, states ...store.FileState) ([]store.FileRecord, error) {
	q := `SELECT ` + fileCols + ` FROM files`
	var args []any
	if len(states) > 0 {
		q += ` WHERE state IN (` + placeholders(len(states)) + `)`
		for _, s := range states {
			args = append(args, string(s))
		}
	}
	q += ` ORDER BY created_at, file_id`
	rows, err := d.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.FileRecord
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateFile reads, mutates and writes the record in one transaction.
// mutate runs inside the transaction and MUST NOT call d (see store.FileStore).
func (d *DB) UpdateFile(ctx context.Context, id string, mutate func(*store.FileRecord) error) (store.FileRecord, error) {
	var out store.FileRecord
	err := inTx(ctx, d.sql, func(tx *sql.Tx) error {
		f, err := scanFile(tx.QueryRowContext(ctx, `SELECT `+fileCols+` FROM files WHERE file_id = ?`, id))
		if err != nil {
			return err
		}
		if err := mutate(&f); err != nil {
			return err
		}
		f.FileID = id
		if err := upsertFile(ctx, tx, f); err != nil {
			return err
		}
		out = f
		return nil
	})
	if err != nil {
		return store.FileRecord{}, err
	}
	return out, nil
}

func (d *DB) InboundBytes(ctx context.Context, peer core.MachineID, since time.Time) (int64, error) {
	var n int64
	err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(SUM(size), 0) FROM files
WHERE peer = ? AND direction = ? AND (state = ? OR (state = ? AND created_at >= ?))`,
		string(peer), string(store.TaskInbound), string(store.FileDownloading), string(store.FileDone),
		toMS(since)).Scan(&n)
	return n, err
}

// PurgeFilesBefore deletes finished file records created before t.
func (d *DB) PurgeFilesBefore(ctx context.Context, t time.Time) (int, error) {
	res, err := d.sql.ExecContext(ctx, `DELETE FROM files WHERE created_at < ? AND state IN (?, ?, ?, ?)`,
		toMS(t), string(store.FileDone), string(store.FileFailed), string(store.FileDeclined), string(store.FileSent))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func scanFile(s rowScanner) (store.FileRecord, error) {
	var (
		f                 store.FileRecord
		dir, peer, state  string
		chunks, nextChunk int64
		createdAt         int64
	)
	if err := s.Scan(&f.FileID, &dir, &peer, &f.MsgID, &f.BlobID, &f.Name, &f.Size, &chunks, &f.SHA256,
		&f.Key, &f.TaskID, &state, &f.LocalPath, &nextChunk, &f.Attempts, &f.Reason, &createdAt); err != nil {
		return store.FileRecord{}, notFound(err)
	}
	f.Direction = store.TaskDirection(dir)
	f.Peer = core.MachineID(peer)
	f.State = store.FileState(state)
	f.Chunks = uint32(chunks)
	f.NextChunk = uint32(nextChunk)
	f.CreatedAt = fromMS(createdAt)
	return f, nil
}
