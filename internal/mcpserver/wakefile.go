package mcpserver

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The wake token reaches the listener through a file only this user can
// read, named on the listener's command line. It is never put on a
// command line itself: Claude Code runs a Bash tool command as
// `zsh -c <command>`, so anything in the command text (a printf or echo
// builtin included) is visible in ps to every local user, and the command
// text is also stored in the chat transcript. The file lives in the
// state directory (0700), is 0600, is written with O_EXCL and is removed
// when the session closes or this MCP server exits.

var errNoWakeDir = errors.New("no wake directory configured")

// WriteWakeFile writes token to a new private file and returns its path.
// A chat keeps at most one: an earlier file is removed.
func (s *Session) WriteWakeFile(token string) (string, error) {
	if s.wakeDir == "" {
		return "", errNoWakeDir
	}
	if err := os.MkdirAll(s.wakeDir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(s.wakeDir, 0o700); err != nil {
		return "", err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	path := filepath.Join(s.wakeDir, "wake-"+hex.EncodeToString(b[:]))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	s.mu.Lock()
	old := s.wakeFile
	s.wakeFile = path
	s.mu.Unlock()
	if old != "" {
		os.Remove(old)
	}
	return path, nil
}

// RemoveWakeFile deletes the chat's wake file, if any.
func (s *Session) RemoveWakeFile() {
	s.mu.Lock()
	path := s.wakeFile
	s.wakeFile = ""
	s.mu.Unlock()
	if path != "" {
		os.Remove(path)
	}
}

// ListenerCommand is the background command that listens with wakeFile.
func (s *Session) ListenerCommand(wakeFile string) string {
	prog := s.listenerProgram
	if prog == "" {
		prog = "cravv-connect"
	}
	return shellQuote(prog) + " listen --wake-file " + shellQuote(wakeFile)
}

// shellQuote quotes s for a POSIX shell when needed.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
