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

// WakeKeeper writes the wake token to a file only this user can read, so
// the listener command carries a path and never the token. Implemented by
// *Session.
type WakeKeeper interface {
	WriteWakeFile(token string) (path string, err error)
	RemoveWakeFile()
	ListenerCommand(wakeFile string) string
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
// server keeps in memory to take the session back after a reconnect, and
// the wake token only when no wake file could be written.
type shareOut struct {
	Session   ipc.SharedSessionView `json:"session"`
	Resumed   bool                  `json:"resumed,omitempty"`
	Note      string                `json:"note,omitempty"`
	Listener  string                `json:"listener"`
	WakeToken string                `json:"wake_token,omitempty"`
	Next      string                `json:"next"`
}

// ResumedNote tells the model a share took over its earlier session.
const ResumedNote = "This chat took over its earlier session of the same name (it was away): its links and anything that waited are kept. Call links and check_inbox."

// Listener instructions returned by session_share.
const (
	ListenerNext = "Now start the listener: run the listener command as a background command (in Claude Code, the Bash tool with run_in_background). " +
		"It exits with one line when something arrives for this session. Then call check_inbox (and review_pending when the line says so), " +
		"handle what arrived, and start the listener again. Start it again after every exit."
	ListenerNextStdin = "Start the listener command as a background command and write the wake_token to its stdin, never as an argument. " +
		"It exits with one line when something arrives; then call check_inbox and start it again. If you cannot run background commands, call wait_for_message instead."
)

func (sessionShareTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "session_share", "Share this chat as a session other machines can link to. Nothing reaches this chat until it shares and a link is accepted. "+
		"Returns the listener command to run in the background.", annLocal,
		func(ctx context.Context, in sessionShareIn) (string, error) {
			var r ipc.ShareResult
			p := ipc.SessionShareParams{Name: in.Name, Purpose: in.Purpose, Visibility: in.Visibility, AgentSession: agentSessionOf(c)}
			if err := c.Call(ctx, ipc.MethodSessionShare, p, &r); err != nil {
				return "", err
			}
			if ra, ok := c.(Reattacher); ok {
				ra.SetReattach(r.ReattachToken)
			}
			out := shareOut{Session: r.Session, Listener: "cravv-connect listen", WakeToken: r.WakeToken, Next: ListenerNextStdin}
			if r.Resumed {
				out.Resumed, out.Note = true, ResumedNote
			}
			if wk, ok := c.(WakeKeeper); ok {
				if path, err := wk.WriteWakeFile(r.WakeToken); err == nil {
					out.Listener, out.WakeToken, out.Next = wk.ListenerCommand(path), "", ListenerNext
				}
			}
			return jsonText(out)
		})
}

type sessionCloseTool struct{}

func (sessionCloseTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "session_close", "Close this chat's session and every link it has.", annCutOff,
		func(ctx context.Context, _ noArgs) (string, error) {
			if err := c.Call(ctx, ipc.MethodSessionClose, nil, nil); err != nil {
				return "", err
			}
			if ra, ok := c.(Reattacher); ok {
				ra.SetReattach("")
			}
			if wk, ok := c.(WakeKeeper); ok {
				wk.RemoveWakeFile()
			}
			return "Session closed. A running listener exits with a line saying so.", nil
		})
}

type sessionSetTool struct{}

type sessionSetIn struct {
	Purpose    *string `json:"purpose,omitempty" jsonschema:"new purpose: one line, at most 120 characters"`
	Visibility *string `json:"visibility,omitempty" jsonschema:"private, all-peers, or peers:<alias>[,<alias>...]"`
}

func (sessionSetTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "session_set", "Change this chat's session purpose or who can see it. Existing links stay.", annLocal,
		func(ctx context.Context, in sessionSetIn) (string, error) {
			return callJSON[ipc.SharedSessionView](ctx, c, ipc.MethodSessionSet, ipc.SessionSetParams{Purpose: in.Purpose, Visibility: in.Visibility})
		})
}

type machinesTool struct{}

func (machinesTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "machines", "List the paired machines (local alias, online, paused). Pairing, pausing and unpairing are for the human (cravv-connect in a terminal).", annRead,
		func(ctx context.Context, _ noArgs) (string, error) {
			return callJSON[ipc.PeerListResult](ctx, c, ipc.MethodMachines, nil)
		})
}

type sessionsTool struct{}

type machineIn struct {
	Machine string `json:"machine" jsonschema:"the paired machine's alias"`
}

func (sessionsTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "sessions", "List the sessions a paired machine lets this machine see. Purposes come from the other machine and are wrapped in <remote_message>.", annReadRemote,
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
	addTool(s, "connect", "Ask a session on a paired machine for a link. The human on that machine decides; the link is pending until then.", annSend,
		func(ctx context.Context, in connectIn) (string, error) {
			return callJSON[ipc.LinkView](ctx, c, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: in.Target, Permission: in.Permission, Note: in.Note})
		})
}

type linksTool struct{}

func (linksTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "links", "List this session's links: peers, permissions, state and presence.", annRead,
		func(ctx context.Context, _ noArgs) (string, error) {
			return callJSON[ipc.LinksResult](ctx, c, ipc.MethodLinks, nil)
		})
}

type linkIn struct {
	Link int64 `json:"link" jsonschema:"link number"`
}

type disconnectTool struct{}

func (disconnectTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "disconnect", "Close a link. Closed links are never reopened; connect again for a new one.", annCutOff,
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
	addTool(s, "restrict", "Lower what the other side of a link may do here (tasks-auto > tasks-ask > messages). Raising is only possible for the human.", annCutOff,
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
