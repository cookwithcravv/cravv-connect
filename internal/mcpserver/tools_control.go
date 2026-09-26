package mcpserver

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type statusTool struct{}

// statusOut is the status plus this session's own name.
type statusOut struct {
	Session string `json:"session"`
	ipc.StatusResult
}

func (statusTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "status", "Show this machine, its shared sessions, paired peers (alias, online, paused) and pending counts.",
		func(ctx context.Context, _ noArgs) (string, error) {
			var st ipc.StatusResult
			if err := c.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
				return "", err
			}
			out := statusOut{StatusResult: st}
			if n, ok := c.(interface{ Name() string }); ok {
				out.Session = n.Name()
			}
			return jsonText(out)
		})
}

type aliasIn struct {
	Alias string `json:"alias" jsonschema:"peer alias"`
}

type pausePeerTool struct{}

func (pausePeerTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "pause_peer", "Stop all traffic with a peer right away. There is no tool to resume it: the human resumes it with `cravv-connect resume-peer <alias>`.",
		func(ctx context.Context, in aliasIn) (string, error) {
			if err := c.Call(ctx, ipc.MethodPeerPause, ipc.AliasParams{Alias: in.Alias}, nil); err != nil {
				return "", err
			}
			return fmt.Sprintf("Paused %s.", in.Alias), nil
		})
}

type unpairPeerTool struct{}

func (unpairPeerTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "unpair_peer", "Remove a peer and delete its keys. Reconnecting needs a new bind code from the humans.",
		func(ctx context.Context, in aliasIn) (string, error) {
			if err := c.Call(ctx, ipc.MethodPeerUnpair, ipc.AliasParams{Alias: in.Alias}, nil); err != nil {
				return "", err
			}
			return fmt.Sprintf("Unpaired %s.", in.Alias), nil
		})
}

type killSwitchTool struct{}

func (killSwitchTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "kill_switch", "Emergency stop: disconnect from every peer and reject all operations until the human resumes with their password.",
		func(ctx context.Context, _ noArgs) (string, error) {
			if err := c.Call(ctx, ipc.MethodKill, nil, nil); err != nil {
				return "", err
			}
			return "Kill switch is on. Only the human can resume (cravv-connect resume).", nil
		})
}
