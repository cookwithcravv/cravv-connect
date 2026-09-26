package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// Caller is what tools use to reach the daemon.
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
}

// Conn is one daemon connection (satisfied by *ipc.Client).
type Conn interface {
	Caller
	Close() error
	Done() <-chan struct{}
}

// Background reattach schedule, the same as the listener's
// (cli/cmd_listen.go): waits double from reattachBackoffMin up to
// reattachBackoffMax.
var (
	reattachBackoffMin = time.Second
	reattachBackoffMax = 30 * time.Second
)

// reattachAttempt bounds one background reconnect and reattach.
const reattachAttempt = 10 * time.Second

// backgroundReattach turns the keeper on; tests of the on-demand path
// (reattach on the next tool call) turn it off.
var backgroundReattach = true

// Session owns the daemon connection and this MCP process's session
// registration. It connects lazily, so the MCP server starts (and lists its
// tools) even when the daemon is down, and it reconnects and re-registers
// after the daemon restarts; the daemon's reclaim grace keeps the same name.
// While the chat shares a session, a keeper goroutine reconnects and
// reattaches as soon as the connection drops, so the session is open again
// without waiting for the chat's next tool call.
type Session struct {
	dial       func(ctx context.Context) (Conn, error)
	projectDir string

	agentSession    string // the agent's own chat ID, for the chat's hooks ("" if unknown)
	runToken        string // a managed run's token ("" for a chat)
	wakeDir         string // where wake files are written ("" for none)
	listenerProgram string // how the listener command names cravv-connect

	mu       sync.Mutex
	agent    string
	conn     Conn
	name     string
	reattach string // reattach token of the session this chat shared ("" if none)
	wakeFile string // the wake file of that session ("" if none)
	// pending is set while the current connection still has to take the
	// session back with the reattach token.
	pending bool
	keeping bool // the keeper goroutine runs
	closed  bool // Close was called: no keeper starts again

	ctx     context.Context // ends at Close; bounds the keeper
	cancel  context.CancelFunc
	keepers sync.WaitGroup
}

// AgentSession returns the agent's own chat ID ("" if unknown).
func (s *Session) AgentSession() string { return s.agentSession }

// NewSession returns an unconnected session for projectDir.
func NewSession(dial func(ctx context.Context) (Conn, error), projectDir string) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{dial: dial, projectDir: projectDir, agent: "agent", ctx: ctx, cancel: cancel}
}

// SetAgent records the MCP client's name; it applies to the next
// registration. Empty names are ignored.
func (s *Session) SetAgent(clientName string) {
	if clientName == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agent = normalizeAgent(clientName)
}

// Name returns the registered session name ("" before registration).
func (s *Session) Name() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// SetReattach records the reattach token of the session this chat shared
// ("" forgets it). It stays in memory only.
func (s *Session) SetReattach(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reattach, s.pending = token, false
	if token != "" && backgroundReattach && !s.keeping && !s.closed {
		s.keeping = true
		s.keepers.Add(1)
		go s.keep()
	}
}

// keep reconnects and reattaches whenever the connection drops while the
// chat shares a session, retrying with backoff. It returns when the token
// is forgotten (the session closed) or at Close.
func (s *Session) keep() {
	defer s.keepers.Done()
	wait := reattachBackoffMin
	for {
		s.mu.Lock()
		if s.reattach == "" || s.closed {
			s.keeping = false
			s.mu.Unlock()
			return
		}
		c, pending := s.conn, s.pending
		s.mu.Unlock()
		if c != nil && !pending {
			select {
			case <-s.ctx.Done():
				s.stopKeeping()
				return
			case <-c.Done():
				wait = reattachBackoffMin
			}
		}
		ctx, cancel := context.WithTimeout(s.ctx, reattachAttempt)
		_, err := s.Connect(ctx)
		cancel()
		s.mu.Lock()
		ok := err == nil && !s.pending
		s.mu.Unlock()
		if ok {
			continue
		}
		select {
		case <-s.ctx.Done():
			s.stopKeeping()
			return
		case <-time.After(wait):
		}
		wait = min(2*wait, reattachBackoffMax)
	}
}

func (s *Session) stopKeeping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keeping = false
}

// Connect makes sure a live, registered connection exists. A chat that
// shared a session takes it back on the new connection with its reattach
// token. If the daemon answers not_found (the session closed meanwhile), the
// token is forgotten and the chat must share again; any other failure keeps
// the token and the reattach is tried again on the next call.
func (s *Session) Connect(ctx context.Context) (Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		select {
		case <-s.conn.Done():
			s.conn.Close()
			s.conn = nil
		default:
			s.reattachLocked(ctx)
			return s.conn, nil
		}
	}
	c, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	var r ipc.SessionRegisterResult
	if err := c.Call(ctx, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: s.agent, ProjectDir: s.projectDir, PID: os.Getpid()}, &r); err != nil {
		c.Close()
		return nil, err
	}
	if s.runToken != "" {
		// A managed run acts only as its run's session. The token is
		// single-use: if this connection drops, a new one cannot bind it
		// again, and the run's tools fail until the run ends.
		if err := c.Call(ctx, ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: s.runToken}, nil); err != nil {
			c.Close()
			return nil, err
		}
		s.conn, s.name = c, r.Name
		return c, nil
	}
	s.conn, s.name = c, r.Name
	s.pending = s.reattach != ""
	s.reattachLocked(ctx)
	return c, nil
}

// reattachLocked takes the shared session back on s.conn if that is still
// pending. s.mu must be held.
func (s *Session) reattachLocked(ctx context.Context) {
	if !s.pending || s.reattach == "" {
		return
	}
	err := s.conn.Call(ctx, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: s.reattach, AgentSession: s.agentSession}, nil)
	switch {
	case err == nil:
		s.pending = false
	case ipc.IsKind(err, ipc.KindNotFound):
		s.reattach, s.pending = "", false
		if s.wakeFile != "" { // the session is gone: so is its listener
			os.Remove(s.wakeFile)
			s.wakeFile = ""
		}
	}
}

// retrySafe lists the read-only methods that may be sent again on a new
// connection when the old one dropped mid-call. Everything else may already
// have taken effect (a message sent, a task claimed, an inbox page marked
// read), so it is not repeated: the error is returned and only the next call
// reconnects.
var retrySafe = map[string]bool{
	ipc.MethodStatus:       true,
	ipc.MethodPeerList:     true,
	ipc.MethodTaskGet:      true,
	ipc.MethodFilesList:    true,
	ipc.MethodHookCounts:   true,
	ipc.MethodLinks:        true,
	ipc.MethodSessionsList: true,
}

// Call runs one daemon call. If the connection dropped, a read-only call is
// retried once on a new connection; any other call fails with an error
// wrapping ipc.ErrClosed, because it may or may not have been applied.
func (s *Session) Call(ctx context.Context, method string, params, result any) error {
	for attempt := 0; ; attempt++ {
		c, err := s.Connect(ctx)
		if err != nil {
			return err
		}
		err = c.Call(ctx, method, params, result)
		if !errors.Is(err, ipc.ErrClosed) {
			return err
		}
		s.drop(c)
		if attempt == 1 {
			return err
		}
		if !retrySafe[method] {
			return fmt.Errorf("%w during %s: it may or may not have been applied; check (for example with check_inbox or get_task) before trying again", err, method)
		}
	}
}

func (s *Session) drop(c Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == c {
		s.conn.Close()
		s.conn = nil
	}
}

// Close ends the connection, which ends the daemon session, and removes
// the wake file.
func (s *Session) Close() {
	s.RemoveWakeFile()
	s.cancel()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.keepers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.conn.Close()
		s.conn = nil
	}
}

var agentClean = regexp.MustCompile(`[^a-z0-9-]+`)

// knownAgents maps MCP clientInfo.name prefixes to short agent names.
var knownAgents = []struct{ prefix, name string }{
	{"claude", "claude"},
	{"codex", "codex"},
	{"cursor", "cursor"},
	{"gemini", "gemini"},
	{"visual-studio-code", "vscode"},
	{"vscode", "vscode"},
	{"github-copilot", "copilot"},
}

// normalizeAgent turns a client name ("claude-code", "codex-mcp-client") into
// the short agent part of a session name.
func normalizeAgent(clientName string) string {
	n := strings.Trim(agentClean.ReplaceAllString(strings.ToLower(strings.TrimSpace(clientName)), "-"), "-")
	for _, k := range knownAgents {
		if strings.HasPrefix(n, k.prefix) {
			return k.name
		}
	}
	if len(n) > 16 {
		n = strings.TrimRight(n[:16], "-")
	}
	if n == "" {
		return "agent"
	}
	return n
}
