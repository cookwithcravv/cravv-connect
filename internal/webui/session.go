package webui

import (
	"crypto/subtle"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Flash kinds.
const (
	flashOK    = "ok"
	flashError = "error"
)

// flash is a one-time message shown on the next page.
type flash struct {
	Kind string
	Text string
}

// uiSession is one browser session: its cookie ID, its CSRF token, its own
// daemon connection (which holds the password unlock) and pending messages.
type uiSession struct {
	id    string
	csrf  string
	c     Caller
	clock core.Clock

	mu       sync.Mutex
	flashes  []flash
	unlocked time.Time
}

func (s *uiSession) csrfOK(tok string) bool {
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.csrf)) == 1
}

func (s *uiSession) addFlash(kind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flashes = append(s.flashes, flash{Kind: kind, Text: text})
}

// takeFlashes returns and clears the pending messages.
func (s *uiSession) takeFlashes() []flash {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flashes
	s.flashes = nil
	return f
}

// setUnlocked records the end of the connection's password window.
func (s *uiSession) setUnlocked(until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlocked = until
}

// unlockedUntil returns the end of the password window, or zero if closed.
func (s *uiSession) unlockedUntil() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clock.Now().Before(s.unlocked) {
		return s.unlocked
	}
	return time.Time{}
}
