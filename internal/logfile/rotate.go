// Package logfile is a small size-based rotating log file for the daemon.
package logfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"
)

// Daemon log defaults: rotate at 10 MiB and keep 3 old files, so the log
// never takes more than about 40 MiB.
const (
	DefaultMaxBytes = 10 << 20
	DefaultKeep     = 3
)

// Rotating is an io.WriteCloser that appends to path and, when a write would
// take the file past maxBytes, renames path to path.1 (path.1 to path.2, and
// so on, dropping anything past path.<keep>) and starts a new file. A single
// write is never split. It is safe for concurrent use.
type Rotating struct {
	mu   sync.Mutex
	path string
	max  int64
	keep int
	f    *os.File
	size int64
}

// Open opens (or creates, mode 0600) path for appending. maxBytes <= 0 and
// keep < 1 use the defaults.
func Open(path string, maxBytes int64, keep int) (*Rotating, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if keep < 1 {
		keep = DefaultKeep
	}
	r := &Rotating{path: path, max: maxBytes, keep: keep}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotating) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

// Write appends p, rotating first when p would not fit.
func (r *Rotating) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, fs.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// rotate shifts path.i to path.i+1, path to path.1, and opens a new path.
func (r *Rotating) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	if err := os.Remove(fmt.Sprintf("%s.%d", r.path, r.keep)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for i := r.keep - 1; i >= 1; i-- {
		from := fmt.Sprintf("%s.%d", r.path, i)
		if err := os.Rename(from, fmt.Sprintf("%s.%d", r.path, i+1)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return r.open()
}

// Close closes the current file.
func (r *Rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
