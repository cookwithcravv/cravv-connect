package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// SessionRegistry names IPC attachments: every connection that registers
// (an agent's MCP server, a CLI run) gets a name, <agent>@<dir>. An
// attachment carries no link traffic; a chat shares a session for that
// (SessionService). A disconnected attachment keeps its name for
// core.ReclaimGrace.
type SessionRegistry struct {
	mu       sync.Mutex
	sessions store.SessionStore
	clock    core.Clock
}

// NewSessionRegistry builds a registry over the session store.
func NewSessionRegistry(sessions store.SessionStore, clock core.Clock) *SessionRegistry {
	return &SessionRegistry{sessions: sessions, clock: clock}
}

// Register returns the name for a new connection. A disconnected
// attachment with the same agent and project folder that was last seen
// within core.ReclaimGrace is reclaimed with its name. Otherwise a new one
// named <agent>@<basename> (plus -2, -3, ...) is created.
func (r *SessionRegistry) Register(ctx context.Context, agent, projectDir string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.sweepLocked(ctx); err != nil {
		return "", err
	}
	now := r.clock.Now()
	all, err := r.sessions.ListSessions(ctx)
	if err != nil {
		return "", err
	}
	var reclaim *store.SessionRecord
	for i := range all {
		s := all[i]
		if s.Connected || s.Agent != agent || s.ProjectDir != projectDir {
			continue
		}
		if now.Sub(s.LastSeen) > core.ReclaimGrace {
			continue
		}
		if reclaim == nil || s.LastSeen.After(reclaim.LastSeen) {
			reclaim = &all[i]
		}
	}
	if reclaim != nil {
		reclaim.Connected = true
		reclaim.LastSeen = now
		if err := r.sessions.PutSession(ctx, *reclaim); err != nil {
			return "", err
		}
		return reclaim.Name, nil
	}
	taken := make(map[string]bool, len(all))
	for _, s := range all {
		taken[s.Name] = true
	}
	base := SessionBaseName(agent, projectDir)
	name := base
	for n := 2; taken[name]; n++ {
		name = fmt.Sprintf("%s-%d", base, n)
	}
	rec := store.SessionRecord{Name: name, Agent: agent, ProjectDir: projectDir, LastSeen: now, Connected: true}
	if err := r.sessions.PutSession(ctx, rec); err != nil {
		return "", err
	}
	return name, nil
}

// Disconnect marks a session disconnected and starts its reclaim grace.
func (r *SessionRegistry) Disconnect(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := r.sessions.GetSession(ctx, name)
	if err != nil {
		return err
	}
	s.Connected = false
	s.LastSeen = r.clock.Now()
	return r.sessions.PutSession(ctx, s)
}

// DisconnectAll marks every session disconnected. The daemon calls it at
// startup: connections do not survive a daemon restart, so each session
// gets the normal reclaim grace from the moment the daemon comes back.
func (r *SessionRegistry) DisconnectAll(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	all, err := r.sessions.ListSessions(ctx)
	if err != nil {
		return err
	}
	now := r.clock.Now()
	for _, s := range all {
		if !s.Connected {
			continue
		}
		s.Connected = false
		s.LastSeen = now
		if err := r.sessions.PutSession(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// Sweep expires sessions that stayed disconnected longer than the grace.
func (r *SessionRegistry) Sweep(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sweepLocked(ctx)
}

func (r *SessionRegistry) sweepLocked(ctx context.Context) error {
	all, err := r.sessions.ListSessions(ctx)
	if err != nil {
		return err
	}
	now := r.clock.Now()
	for _, s := range all {
		if s.Connected || now.Sub(s.LastSeen) <= core.ReclaimGrace {
			continue
		}
		if err := r.sessions.DeleteSession(ctx, s.Name); err != nil {
			return err
		}
	}
	return nil
}

// Get returns a session record (core.ErrNoSession if unknown).
func (r *SessionRegistry) Get(ctx context.Context, name string) (store.SessionRecord, error) {
	s, err := r.sessions.GetSession(ctx, name)
	if errors.Is(err, core.ErrNotFound) {
		return s, core.ErrNoSession
	}
	return s, err
}

// Exists reports whether the session is known (connected or within grace).
func (r *SessionRegistry) Exists(ctx context.Context, name string) bool {
	_, err := r.sessions.GetSession(ctx, name)
	return err == nil
}

// Connected reports whether the session currently has a live connection.
func (r *SessionRegistry) Connected(ctx context.Context, name string) bool {
	s, err := r.sessions.GetSession(ctx, name)
	return err == nil && s.Connected
}

// List returns every known session.
func (r *SessionRegistry) List(ctx context.Context) ([]store.SessionRecord, error) {
	return r.sessions.ListSessions(ctx)
}

// SessionBaseName is "<agent>@<basename(projectDir)>", each part lowercased
// and limited to [a-z0-9._-] (other runs become '-'), at most 32 chars each.
func SessionBaseName(agent, projectDir string) string {
	a := sanitizeSessionPart(agent, "agent")
	d := sanitizeSessionPart(filepath.Base(filepath.Clean(projectDir)), "root")
	return a + "@" + d
}

func sanitizeSessionPart(s, fallback string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(s) {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			if !dash {
				b.WriteByte('-')
				dash = true
			}
			continue
		}
		b.WriteRune(c)
		dash = c == '-'
	}
	out := strings.Trim(b.String(), "-.")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-.")
	}
	if out == "" {
		return fallback
	}
	return out
}
