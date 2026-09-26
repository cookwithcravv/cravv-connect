package webui

import (
	"crypto/subtle"
	"sync"
	"time"
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

// uiSession is one browser session: its CSRF token, its own daemon
// connection (never unlocked: password actions use connections of their
// own), its pairings in progress and pending messages. Its cookie ID is the
// server's map key and changes with every password action.
type uiSession struct {
	c Caller

	mu      sync.Mutex
	csrf    string
	flashes []flash
	flows   map[string]*pairFlow
	flowIDs []string // flow IDs, oldest first
	ended   bool
}

// pairFlow is one pairing in progress. Pairing spans several requests (the
// code, the wait, the name), so it keeps its own daemon connection,
// unlocked by the password that started it, until it is finalized, expires
// or its browser session ends. The daemon's pending ID stays here; the page
// only sees the flow ID.
type pairFlow struct {
	c         Caller
	pendingID string
	expires   time.Time
}

func newUISession(c Caller, csrf string) *uiSession {
	return &uiSession{c: c, csrf: csrf, flows: map[string]*pairFlow{}}
}

func (s *uiSession) csrfToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.csrf
}

func (s *uiSession) csrfOK(tok string) bool {
	want := s.csrfToken()
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1
}

func (s *uiSession) setCSRF(tok string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.csrf = tok
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

// addFlow keeps a new pairing, ending the oldest one beyond MaxPairFlows.
// It returns false (and the caller closes f's connection) if the session
// has already ended.
func (s *uiSession) addFlow(id string, f *pairFlow) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return false
	}
	s.flows[id] = f
	s.flowIDs = append(s.flowIDs, id)
	for len(s.flowIDs) > MaxPairFlows {
		s.endFlowLocked(s.flowIDs[0])
	}
	return true
}

// flow returns the live pairing called id, or nil. An expired one is ended.
func (s *uiSession) flow(id string, now time.Time) *pairFlow {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.flows[id]
	if !ok {
		return nil
	}
	if !now.Before(f.expires) {
		s.endFlowLocked(id)
		return nil
	}
	return f
}

// endFlow ends a pairing and closes its connection.
func (s *uiSession) endFlow(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.endFlowLocked(id)
}

func (s *uiSession) endFlowLocked(id string) {
	if f, ok := s.flows[id]; ok {
		f.c.Close()
		delete(s.flows, id)
	}
	for i, fid := range s.flowIDs {
		if fid == id {
			s.flowIDs = append(s.flowIDs[:i:i], s.flowIDs[i+1:]...)
			break
		}
	}
}

// sweepFlows ends the expired pairings.
func (s *uiSession) sweepFlows(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, f := range s.flows {
		if !now.Before(f.expires) {
			s.endFlowLocked(id)
		}
	}
}

// end closes the session's connection and its pairings' connections.
func (s *uiSession) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	for id := range s.flows {
		s.endFlowLocked(id)
	}
	s.c.Close()
}
