package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Reattacher keeps the reattach token of the session this chat shared.
// Implemented by *Session.
type Reattacher interface {
	SetReattach(token string)
}

// agentSessionOf returns the agent's own chat ID when c knows it.
func agentSessionOf(c Caller) string {
	if a, ok := c.(interface{ AgentSession() string }); ok {
		return a.AgentSession()
	}
	return ""
}

type sessionShareTool struct{}

type sessionShareIn struct {
	Name       string `json:"name" jsonschema:"session name: 1 to 32 characters of a-z, 0-9 and -"`
	Purpose    string `json:"purpose,omitempty" jsonschema:"one line, at most 120 characters, shown to machines that can see the session"`
	Visibility string `json:"visibility,omitempty" jsonschema:"private (default), all-peers, or peers:<alias>[,<alias>...]"`
}

// shareOut is what the model sees: never the reattach token, which the MCP
// server keeps in memory to take the session back after a reconnect.
type shareOut struct {
	Session   ipc.SharedSessionView `json:"session"`
	WakeToken string                `json:"wake_token"`
}

func (sessionShareTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "session_share", "Share this chat as a session other machines can link to. Nothing reaches this chat until it shares and a link is accepted. Returns the wake token for the listener.",
		func(ctx context.Context, in sessionShareIn) (string, error) {
			var r ipc.ShareResult
			p := ipc.SessionShareParams{Name: in.Name, Purpose: in.Purpose, Visibility: in.Visibility, AgentSession: agentSessionOf(c)}
			if err := c.Call(ctx, ipc.MethodSessionShare, p, &r); err != nil {
				return "", err
			}
			if ra, ok := c.(Reattacher); ok {
				ra.SetReattach(r.ReattachToken)
			}
			return jsonText(shareOut{Session: r.Session, WakeToken: r.WakeToken})
		})
}

type sessionCloseTool struct{}

func (sessionCloseTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "session_close", "Close this chat's session and every link it has.",
		func(ctx context.Context, _ noArgs) (string, error) {
			if err := c.Call(ctx, ipc.MethodSessionClose, nil, nil); err != nil {
				return "", err
			}
			if ra, ok := c.(Reattacher); ok {
				ra.SetReattach("")
			}
			return "Session closed.", nil
		})
}

type sessionsTool struct{}

type machineIn struct {
	Machine string `json:"machine" jsonschema:"the paired machine's alias"`
}

func (sessionsTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "sessions", "List the sessions a paired machine lets this machine see. Purposes come from the other machine and are wrapped in <remote_message>.",
		func(ctx context.Context, in machineIn) (string, error) {
			return callJSON[ipc.SessionsListResult](ctx, c, ipc.MethodSessionsList, ipc.MachineParams{Machine: in.Machine})
		})
}

type connectTool struct{}

type connectIn struct {
	Target     string `json:"target" jsonschema:"machine/session"`
	Permission string `json:"permission" jsonschema:"what you ask to do there: messages, tasks-ask or tasks-auto"`
	Note       string `json:"note,omitempty" jsonschema:"shown to the other human, at most 280 characters"`
}

func (connectTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "connect", "Ask a session on a paired machine for a link. The human on that machine decides; the link is pending until then.",
		func(ctx context.Context, in connectIn) (string, error) {
			return callJSON[ipc.LinkView](ctx, c, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: in.Target, Permission: in.Permission, Note: in.Note})
		})
}

type linksTool struct{}

func (linksTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "links", "List this session's links: peers, permissions, state and presence.",
		func(ctx context.Context, _ noArgs) (string, error) {
			return callJSON[ipc.LinksResult](ctx, c, ipc.MethodLinks, nil)
		})
}

type linkIn struct {
	Link int64 `json:"link" jsonschema:"link number"`
}

type disconnectTool struct{}

func (disconnectTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "disconnect", "Close a link. Closed links are never reopened; connect again for a new one.",
		func(ctx context.Context, in linkIn) (string, error) {
			if err := c.Call(ctx, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: in.Link}, nil); err != nil {
				return "", err
			}
			return fmt.Sprintf("Link %d closed.", in.Link), nil
		})
}

type restrictTool struct{}

type restrictIn struct {
	Link       int64  `json:"link" jsonschema:"link number"`
	Permission string `json:"permission" jsonschema:"messages or tasks-ask (lower than now)"`
}

func (restrictTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "restrict", "Lower what the other side of a link may do here (tasks-auto > tasks-ask > messages). Raising is only possible for the human.",
		func(ctx context.Context, in restrictIn) (string, error) {
			var v ipc.LinkView
			err := c.Call(ctx, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: in.Link, Permission: in.Permission}, &v)
			if errors.Is(err, core.ErrAuthRequired) {
				return "", fmt.Errorf("raising a link needs the human's password: ask them to run `cravv-connect link permit %d %s` in their terminal", in.Link, in.Permission)
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Link %d now allows %s.", v.Link, v.PermissionIn), nil
		})
}
