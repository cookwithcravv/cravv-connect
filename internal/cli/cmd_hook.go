package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

// hookTimeout bounds the whole hook so it never slows the agent down.
var hookTimeout = 2 * time.Second

// maxHookInput caps how much hook JSON is read.
const maxHookInput = 1 << 20

func init() { Register(newHookCmd) }

// hookInput is the subset of agent hook JSON we use (Claude Code sends it on
// stdin; Codex `notify` passes it as the last argument).
type hookInput struct {
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	StopHookActive bool   `json:"stop_hook_active"`
}

// hookRenderer turns counts into hook output for one event. Output must never
// contain message bodies or peer-chosen names; the notice uses aliases only.
type hookRenderer func(in hookInput, res ipc.HookCountsResult) string

// hookRenderers maps hook_event_name to its renderer; others use renderLine.
var hookRenderers = map[string]hookRenderer{
	"Stop":         renderStop,
	"SubagentStop": renderStop,
}

// renderLine prints the notice as plain text. For Claude Code's
// UserPromptSubmit, plain stdout on exit 0 is added to the model's context.
func renderLine(_ hookInput, res ipc.HookCountsResult) string {
	if res.Notice == "" {
		return ""
	}
	return res.Notice + "\n"
}

// renderStop handles Claude Code's Stop hook, whose plain stdout only reaches
// the debug log. When the daemon says to keep the chat going (its shared
// session has unhandled items), it returns {"decision":"block","reason":...}
// with a one-line reason. The daemon never blocks for decisions only,
// respects stop_hook_active and stops after 2 blocks in a row.
func renderStop(_ hookInput, res ipc.HookCountsResult) string {
	if !res.Block || res.Reason == "" {
		return ""
	}
	b, err := json.Marshal(map[string]string{"decision": "block", "reason": res.Reason})
	if err != nil {
		return ""
	}
	return string(b) + "\n"
}

func newHookCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "hook",
		Short: "Print a one-line unread notice for agent hooks (always exits 0)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprint(env.Stdout, hookOutput(cmd.Context(), env, args))
			return nil
		},
	}
}

// hookOutput never fails: any problem (daemon down, bad input, timeout) means
// no output.
func hookOutput(ctx context.Context, env *Env, args []string) string {
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	in := readHookInput(env, args)
	if in.Cwd == "" {
		if wd, err := env.Getwd(); err == nil {
			in.Cwd = wd
		}
	}
	c, err := connect(ctx, env)
	if err != nil {
		return ""
	}
	defer c.Close()
	var res ipc.HookCountsResult
	q := ipc.HookCountsParams{Cwd: in.Cwd, SessionID: in.SessionID, Event: in.HookEventName, StopHookActive: in.StopHookActive}
	if err := c.Call(ctx, ipc.MethodHookCounts, q, &res); err != nil {
		return ""
	}
	render, ok := hookRenderers[in.HookEventName]
	if !ok {
		render = renderLine
	}
	return render(in, res)
}

func readHookInput(env *Env, args []string) hookInput {
	var in hookInput
	if len(args) > 0 && strings.HasPrefix(strings.TrimSpace(args[len(args)-1]), "{") {
		json.Unmarshal([]byte(args[len(args)-1]), &in)
		return in
	}
	b, err := io.ReadAll(io.LimitReader(env.Stdin, maxHookInput))
	if err == nil {
		json.Unmarshal(b, &in)
	}
	return in
}
