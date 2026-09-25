package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// SessionRegistry names agent sessions, tracks which are connected, and
// reclaims or expires disconnected sessions (spec 8.1).
type SessionRegistry struct {
	mu        sync.Mutex
	sessions  store.SessionStore
	inbox     store.InboxStore
	clock     core.Clock
	onExpired []func(ctx context.Context, rec store.SessionRecord)
}

// CLIAgent is the agent name the `--json` CLI registers with. Each CLI
// invocation is a short-lived session, so a disconnected cli session stays
// reclaimable (same name, cursor and claimed tasks) for core.InboxRetention
// instead of core.ReclaimGrace, and its claimed tasks are never abandoned.
const CLIAgent = "cli"

// graceFor is how long a disconnected session of this agent stays reclaimable.
func graceFor(agent string) time.Duration {
	if agent == CLIAgent {
		return core.InboxRetention
	}
	return core.ReclaimGrace
}

// NewSessionRegistry builds a registry over the session and inbox stores.
func NewSessionRegistry(sessions store.SessionStore, inbox store.InboxStore, clock core.Clock) *SessionRegistry {
	return &SessionRegistry{sessions: sessions, inbox: inbox, clock: clock}
}

// OnExpired adds a callback run (in registration order) when a disconnected
// session outlives its grace, before the session is deleted. Callbacks must
// not call back into the registry.
func (r *SessionRegistry) OnExpired(fn func(ctx context.Context, rec store.SessionRecord)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onExpired = append(r.onExpired, fn)
}

// Register returns the session name for a new connection. A disconnected
// session with the same agent and project folder that was last seen within
// its grace (core.ReclaimGrace; core.InboxRetention for CLIAgent) is
// reclaimed with its name and cursor. Otherwise a new
// session named <agent>@<basename> (plus -2, -3, ...) is created whose
// cursor starts at InitialCursor(now - core.NewSessionBacklog).
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
		if now.Sub(s.LastSeen) > graceFor(s.Agent) {
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
	cursor, err := r.inbox.InitialCursor(ctx, now.Add(-core.NewSessionBacklog))
	if err != nil {
		return "", err
	}
	rec := store.SessionRecord{Name: name, Agent: agent, ProjectDir: projectDir, Cursor: cursor, LastSeen: now, Connected: true}
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
		if s.Connected || now.Sub(s.LastSeen) <= graceFor(s.Agent) {
			continue
		}
		for _, fn := range r.onExpired {
			fn(ctx, s)
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

// ForProjectDir returns a connected session registered for projectDir.
// Hooks use it to count unread items for their cwd.
func (r *SessionRegistry) ForProjectDir(ctx context.Context, projectDir string) (string, bool) {
	all, err := r.sessions.ListSessions(ctx)
	if err != nil {
		return "", false
	}
	for _, s := range all {
		if s.Connected && s.ProjectDir == projectDir {
			return s.Name, true
		}
	}
	return "", false
}

// List returns every known session.
func (r *SessionRegistry) List(ctx context.Context) ([]store.SessionRecord, error) {
	return r.sessions.ListSessions(ctx)
}

// SetCursor stores a session's read position.
func (r *SessionRegistry) SetCursor(ctx context.Context, name string, cursor int64) error {
	return r.sessions.SetCursor(ctx, name, cursor)
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
