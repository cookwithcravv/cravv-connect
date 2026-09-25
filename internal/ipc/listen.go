package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// ErrAlreadyRunning is returned by Listen when a live daemon owns the socket.
var ErrAlreadyRunning = errors.New("daemon already running")

// Listen creates the socket directory (0700), removes a stale socket left by a
// crashed daemon, listens, and sets the socket file to 0600. It refuses to
// remove anything at the path that is not a socket.
func Listen(socketPath string) (net.Listener, error) {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if fi, err := os.Lstat(socketPath); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace %s: not a socket", socketPath)
		}
		if c, err := net.DialTimeout("unix", socketPath, time.Second); err == nil {
			c.Close()
			return nil, ErrAlreadyRunning
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}
