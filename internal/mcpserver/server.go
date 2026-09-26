// Package mcpserver is the stdio MCP server agents start with
// `cravv-connect mcp`. It holds no state; every tool calls the daemon.
package mcpserver

import (
	"context"
	"log/slog"

	"github.com/cravv/cravv-connect/internal/present"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configures the MCP server.
type Options struct {
	Dial       func(ctx context.Context) (Conn, error)
	ProjectDir string
	Version    string
	Logger     *slog.Logger
	// AgentSession is the agent's own chat ID (Claude Code sets
	// CLAUDE_CODE_SESSION_ID for its MCP servers). The daemon records it
	// with the shared session so the chat's hooks find it.
	AgentSession string
}

// New builds the MCP server and its daemon session.
//
// The agent name comes from the client's clientInfo.name. Clients on the
// legacy handshake (initialize, then notifications/initialized) register
// eagerly from InitializedHandler. Clients on protocol 2026-07-28 or later
// skip that handshake and send clientInfo in each request's _meta, so the
// session registers lazily on the first tool call; a receiving middleware
// records the name before any tool runs. ServerRequest.ClientInfo covers both.
func New(opts Options) (*mcp.Server, *Session) {
	sess := NewSession(opts.Dial, opts.ProjectDir)
	sess.agentSession = opts.AgentSession
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "cravv-connect", Version: opts.Version}, &mcp.ServerOptions{
		Instructions: present.Instructions,
		InitializedHandler: func(ctx context.Context, req *mcp.InitializedRequest) {
			onInitialized(ctx, sess, req.Session.InitializeParams(), logger)
		},
	})
	srv.AddReceivingMiddleware(agentNameMiddleware(sess))
	for _, t := range Tools() {
		t.Register(srv, sess)
	}
	return srv, sess
}

// onInitialized registers the session as soon as a legacy client is ready.
func onInitialized(ctx context.Context, sess *Session, p *mcp.InitializeParams, logger *slog.Logger) {
	if p != nil && p.ClientInfo != nil {
		sess.SetAgent(p.ClientInfo.Name)
	}
	if _, err := sess.Connect(ctx); err != nil {
		logger.Info("daemon session not registered yet", "err", err)
	}
}

// agentNameMiddleware records the client name from every tool call.
func agentNameMiddleware(sess *Session) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if call, ok := req.(*mcp.CallToolRequest); ok {
				if info := call.ClientInfo(); info != nil {
					sess.SetAgent(info.Name)
				}
			}
			return next(ctx, method, req)
		}
	}
}

// Run serves MCP over stdio until the client disconnects.
func Run(ctx context.Context, opts Options) error {
	srv, sess := New(opts)
	defer sess.Close()
	return srv.Run(ctx, &mcp.StdioTransport{})
}
