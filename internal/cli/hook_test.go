package cli

import (
	"encoding/json"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func hookDaemon(t *testing.T, res ipc.HookCountsResult, got *ipc.HookCountsParams) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodHookCounts, ipc.GateAllowWhenKilled, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		json.Unmarshal(raw, got)
		return res, nil
	})
	fd.start()
	return fd
}

func TestHookOutputs(t *testing.T) {
	notice := "cravv-connect: 2 new messages on link 1 from gpu-box. Call check_inbox."
	withUnread := ipc.HookCountsResult{Notice: notice, Unread: 2}
	blocking := ipc.HookCountsResult{Notice: notice, Unread: 2, Block: true, Reason: notice + " Then start the listener again."}
	cases := []struct {
		name  string
		res   ipc.HookCountsResult
		stdin string
		args  []string
		want  string
		query ipc.HookCountsParams
	}{
		{"prompt submit prints line", withUnread, `{"cwd":"/p","session_id":"c1","hook_event_name":"UserPromptSubmit","prompt":"secret"}`, nil, notice + "\n",
			ipc.HookCountsParams{Cwd: "/p", SessionID: "c1", Event: "UserPromptSubmit"}},
		{"nothing prints nothing", ipc.HookCountsResult{}, `{"cwd":"/p","hook_event_name":"UserPromptSubmit"}`, nil, "",
			ipc.HookCountsParams{Cwd: "/p", Event: "UserPromptSubmit"}},
		{"stop blocks with a one-line reason", blocking, `{"cwd":"/p","session_id":"c1","hook_event_name":"Stop","stop_hook_active":true}`, nil,
			`{"decision":"block","reason":"` + notice + ` Then start the listener again."}` + "\n",
			ipc.HookCountsParams{Cwd: "/p", SessionID: "c1", Event: "Stop", StopHookActive: true}},
		{"stop the daemon does not block stays silent", withUnread, `{"cwd":"/p","session_id":"c1","hook_event_name":"Stop"}`, nil, "",
			ipc.HookCountsParams{Cwd: "/p", SessionID: "c1", Event: "Stop"}},
		{"codex notify passes json as argument", withUnread, "", []string{`{"type":"agent-turn-complete","cwd":"/p"}`}, notice + "\n",
			ipc.HookCountsParams{Cwd: "/p"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got ipc.HookCountsParams
			fd := hookDaemon(t, tc.res, &got)
			r := fd.runStdin(nil, tc.stdin, append([]string{"hook"}, tc.args...)...)
			if r.code != 0 || r.stdout != tc.want || r.stderr != "" {
				t.Fatalf("code %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
			}
			if got != tc.query {
				t.Fatalf("query %+v, want %+v", got, tc.query)
			}
		})
	}
}

func TestHookFallsBackToWorkingDirAndIsSilentOnFailure(t *testing.T) {
	var got ipc.HookCountsParams
	fd := hookDaemon(t, ipc.HookCountsResult{}, &got)
	if r := fd.runStdin(nil, "not json", "hook"); r.code != 0 || r.stdout != "" || got.Cwd != "/work/glow-v2" {
		t.Fatalf("%d %q cwd %q", r.code, r.stdout, got.Cwd)
	}
	down := newFakeDaemon(t) // never started
	r := down.runStdin(nil, `{"cwd":"/p","hook_event_name":"UserPromptSubmit"}`, "hook")
	if r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("daemon down: %d %q %q", r.code, r.stdout, r.stderr)
	}
}
