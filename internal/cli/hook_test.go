package cli

import (
	"encoding/json"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func hookDaemon(t *testing.T, res ipc.HookCountsResult, gotCwd *string) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodHookCounts, ipc.GateAllowWhenKilled, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.HookCountsParams
		json.Unmarshal(raw, &p)
		*gotCwd = p.Cwd
		return res, nil
	})
	fd.start()
	return fd
}

func TestHookOutputs(t *testing.T) {
	notice := "cravv-connect: 2 new messages from gpu-box. Use check_inbox."
	withUnread := ipc.HookCountsResult{Notice: notice, Unread: 2}
	onlyApprovals := ipc.HookCountsResult{Notice: "cravv-connect: 1 task awaiting your approval.", Approvals: 1}
	cases := []struct {
		name  string
		res   ipc.HookCountsResult
		stdin string
		args  []string
		want  string
	}{
		{"prompt submit prints line", withUnread, `{"cwd":"/p","hook_event_name":"UserPromptSubmit","prompt":"secret"}`, nil, notice + "\n"},
		{"nothing unread prints nothing", ipc.HookCountsResult{}, `{"cwd":"/p","hook_event_name":"UserPromptSubmit"}`, nil, ""},
		{"stop with unread continues", withUnread, `{"cwd":"/p","hook_event_name":"Stop","stop_hook_active":false}`, nil,
			`{"hookSpecificOutput":{"additionalContext":"` + notice + `","hookEventName":"Stop"}}` + "\n"},
		{"stop already continuing stays silent", withUnread, `{"cwd":"/p","hook_event_name":"Stop","stop_hook_active":true}`, nil, ""},
		{"stop with only approvals stays silent", onlyApprovals, `{"cwd":"/p","hook_event_name":"Stop"}`, nil, ""},
		{"approvals on prompt submit", onlyApprovals, `{"cwd":"/p","hook_event_name":"UserPromptSubmit"}`, nil, "cravv-connect: 1 task awaiting your approval.\n"},
		{"codex notify passes json as argument", withUnread, "", []string{`{"type":"agent-turn-complete","cwd":"/p"}`}, notice + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cwd string
			fd := hookDaemon(t, tc.res, &cwd)
			r := fd.runStdin(nil, tc.stdin, append([]string{"hook"}, tc.args...)...)
			if r.code != 0 || r.stdout != tc.want || r.stderr != "" {
				t.Fatalf("code %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
			}
			if cwd != "/p" {
				t.Fatalf("cwd %q", cwd)
			}
		})
	}
}

func TestHookFallsBackToWorkingDirAndIsSilentOnFailure(t *testing.T) {
	var cwd string
	fd := hookDaemon(t, ipc.HookCountsResult{}, &cwd)
	if r := fd.runStdin(nil, "not json", "hook"); r.code != 0 || r.stdout != "" || cwd != "/work/glow-v2" {
		t.Fatalf("%d %q cwd %q", r.code, r.stdout, cwd)
	}
	down := newFakeDaemon(t) // never started
	r := down.runStdin(nil, `{"cwd":"/p","hook_event_name":"UserPromptSubmit"}`, "hook")
	if r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("daemon down: %d %q %q", r.code, r.stdout, r.stderr)
	}
}
