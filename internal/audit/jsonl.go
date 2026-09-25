package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
)

// FileLogger appends one JSON object per line to a 0600 file.
type FileLogger struct {
	path  string
	clock core.Clock
	mu    sync.Mutex
}

var _ Logger = (*FileLogger)(nil)

func NewFileLogger(path string, clock core.Clock) *FileLogger {
	return &FileLogger{path: path, clock: clock}
}

// Record stamps e.TS (UTC) when it is zero and appends e as one line.
// The file is opened per call with O_APPEND so external rotation is safe.
func (l *FileLogger) Record(e Event) error {
	if e.TS.IsZero() {
		e.TS = l.clock.Now()
	}
	e.TS = e.TS.UTC()
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit: encode: %w", err)
	}
	line = append(line, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("audit: open: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("audit: chmod: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("audit: write: %w", err)
	}
	return f.Close()
}

// maxLine bounds one audit line when reading (events are small; this only
// protects the reader from a corrupted file).
const maxLine = 1 << 20

// ReadEvents returns the last limit events in file order (limit 0 = all).
// A missing file yields no events. Lines that do not parse (for example a
// line cut short by a crash) are skipped.
func ReadEvents(path string, limit int) ([]Event, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("audit: open: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	var out []Event
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) > 2*limit {
			out = append(out[:0], out[len(out)-limit:]...)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("audit: read: %w", err)
	}
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}
