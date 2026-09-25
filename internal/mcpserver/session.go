package mcpserver

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"

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

// Session owns the daemon connection and this MCP process's session
// registration. It connects lazily, so the MCP server starts (and lists its
// tools) even when the daemon is down, and it reconnects and re-registers
// after the daemon restarts; the daemon's reclaim grace keeps the same name.
type Session struct {
	dial       func(ctx context.Context) (Conn, error)
	projectDir string

	mu    sync.Mutex
	agent string
	conn  Conn
	name  string
}

// NewSession returns an unconnected session for projectDir.
func NewSession(dial func(ctx context.Context) (Conn, error), projectDir string) *Session {
	return &Session{dial: dial, projectDir: projectDir, agent: "agent"}
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

// Connect makes sure a live, registered connection exists.
func (s *Session) Connect(ctx context.Context) (Conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		select {
		case <-s.conn.Done():
			s.conn.Close()
			s.conn = nil
		default:
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
	s.conn, s.name = c, r.Name
	return c, nil
}

// Call runs one daemon call, reconnecting once if the connection dropped.
func (s *Session) Call(ctx context.Context, method string, params, result any) error {
	for attempt := 0; ; attempt++ {
		c, err := s.Connect(ctx)
		if err != nil {
			return err
		}
		err = c.Call(ctx, method, params, result)
		if !errors.Is(err, ipc.ErrClosed) || attempt == 1 {
			return err
		}
		s.drop(c)
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

// Close ends the connection, which ends the daemon session.
func (s *Session) Close() {
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
