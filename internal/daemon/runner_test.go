//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/fakeagent"
)

// fakeAgent returns the command that runs this test binary as the fake
// agent in mode, and the file its runs log to.
func fakeAgent(t *testing.T, mode string) (AgentCommand, []string, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "agent.log")
	env := append(os.Environ(), fakeagent.EnvMode+"="+mode, fakeagent.EnvLog+"="+log)
	return AgentCommand{Path: self, Args: []string{"-p", "--session-id", "u-1"}, Dir: t.TempDir(), Stdin: "hello agent"}, env, log
}

func records(t *testing.T, log string) []fakeagent.Record {
	t.Helper()
	recs, err := fakeagent.Records(log)
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// gone reports whether no process has pid any more.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func waitGone(t *testing.T, pids ...int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for _, pid := range pids {
		for !gone(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("process %d still running", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestExecRunnerRunsTheAgent(t *testing.T) {
	cmd, env, log := fakeAgent(t, "ok")
	out := ExecRunner{}.Run(context.Background(), cmd, env, time.Minute)
	if out.Err != nil || out.ExitCode != 0 || out.TimedOut || out.Stopped {
		t.Fatalf("outcome %+v", out)
	}
	if r := (ClaudeAdapter{}).Result(out.Stdout); !r.Parsed || r.SessionID != "u-1" || r.Turns != 2 {
		t.Fatalf("result %+v from %q", r, out.Stdout)
	}
	recs := records(t, log)
	if len(recs) != 1 || recs[0].Prompt != "hello agent" || recs[0].Dir != resolve(cmd.Dir) {
		t.Fatalf("the agent saw %+v", recs)
	}

	cmd, env, _ = fakeAgent(t, "fail")
	out = ExecRunner{}.Run(context.Background(), cmd, env, time.Minute)
	if out.Err != nil || out.ExitCode != 3 || !strings.Contains(out.Stderr, "failing on purpose") {
		t.Fatalf("failing agent %+v", out)
	}
	cmd.Path = filepath.Join(t.TempDir(), "no-such-agent")
	if out := (ExecRunner{}).Run(context.Background(), cmd, env, time.Minute); out.Err == nil || out.ExitCode != -1 {
		t.Fatalf("missing program %+v", out)
	}
}

func TestExecRunnerKillsTheProcessGroup(t *testing.T) {
	cmd, env, log := fakeAgent(t, "hang")
	start := time.Now()
	out := ExecRunner{}.Run(context.Background(), cmd, env, 700*time.Millisecond)
	if !out.TimedOut || out.Stopped || out.ExitCode != -1 || time.Since(start) > 10*time.Second {
		t.Fatalf("outcome %+v after %s", out, time.Since(start))
	}
	recs := records(t, log)
	if len(recs) != 1 || recs[0].ChildPID == 0 {
		t.Fatalf("records %+v", recs)
	}
	waitGone(t, recs[0].PID, recs[0].ChildPID)

	cmd, env, log = fakeAgent(t, "hang")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if recs, _ := fakeagent.Records(log); len(recs) > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
	}()
	out = ExecRunner{}.Run(ctx, cmd, env, time.Minute)
	if !out.Stopped || out.TimedOut {
		t.Fatalf("stopped outcome %+v", out)
	}
	recs = records(t, log)
	waitGone(t, recs[0].PID, recs[0].ChildPID)
}

func TestCapBuffer(t *testing.T) {
	head := &capBuffer{max: 4}
	tail := &capBuffer{max: 4, tail: true}
	for _, s := range []string{"ab", "cdef", "gh"} {
		head.Write([]byte(s))
		tail.Write([]byte(s))
	}
	if string(head.bytes()) != "abcd" || string(tail.bytes()) != "efgh" {
		t.Fatalf("head %q tail %q", head.bytes(), tail.bytes())
	}
}

// A run that ends normally still ends everything it left in its process
// group, and OnStart learns the group.
func TestExecRunnerEndsTheGroupAfterANormalExit(t *testing.T) {
	cmd, env, log := fakeAgent(t, "bg")
	var group int
	cmd.OnStart = func(pgid int) { group = pgid }
	out := ExecRunner{}.Run(context.Background(), cmd, env, time.Minute)
	if out.Err != nil || out.ExitCode != 0 || out.TimedOut || out.Stopped {
		t.Fatalf("outcome %+v", out)
	}
	recs := records(t, log)
	if len(recs) != 1 || recs[0].ChildPID == 0 {
		t.Fatalf("records %+v", recs)
	}
	if group != recs[0].PID {
		t.Fatalf("OnStart saw group %d, the agent is %d", group, recs[0].PID)
	}
	waitGone(t, recs[0].ChildPID)
}
