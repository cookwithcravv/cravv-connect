package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
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
	addTool(s, "status", "Show this machine, this session, paired peers (alias, trust, online, paused) and pending counts.",
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
	addTool(s, "pause_peer", "Stop all traffic with a peer right away. Only the human can resume it.",
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

type lowerTrustTool struct{}

type lowerTrustIn struct {
	Alias string `json:"alias" jsonschema:"peer alias"`
	Level string `json:"level" jsonschema:"chat-only or ask-first"`
}

func (lowerTrustTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "lower_trust", "Lower what a peer may do on this machine (autonomous > ask-first > chat-only). Raising trust is only possible for the human.",
		func(ctx context.Context, in lowerTrustIn) (string, error) {
			err := c.Call(ctx, ipc.MethodPeerTrust, ipc.PeerTrustParams{Alias: in.Alias, Level: in.Level}, nil)
			if errors.Is(err, core.ErrAuthRequired) {
				return "", errors.New("raising trust needs the human's password: ask them to run `cravv-connect trust` in their terminal")
			}
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Trust for %s is now %s.", in.Alias, in.Level), nil
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
