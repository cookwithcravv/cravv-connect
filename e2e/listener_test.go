package e2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/cli"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// CLIRun is one finished cravv-connect command run against a node.
type CLIRun struct {
	Code   int
	Stdout string
	Stderr string
}

// RunCLI runs the real cravv-connect command line against node n's daemon
// with stdin, in the node's project folder.
func (n *Node) RunCLI(stdin string, args ...string) CLIRun {
	var out, errb bytes.Buffer
	env := &cli.Env{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		Paths: func() (config.Paths, error) { return n.Paths, nil },
		Getwd: func() (string, error) { return n.Proj, nil },
		Dial:  func(ctx context.Context) (cli.Caller, error) { return ipc.DialContext(ctx, n.Paths.Socket) },
	}
	code := cli.Main(args, env)
	return CLIRun{Code: code, Stdout: out.String(), Stderr: errb.String()}
}

// StartListener runs `cravv-connect listen` with the wake token on stdin in
// the background, as the agent does, and returns its result channel.
func (n *Node) StartListener(token string) <-chan CLIRun {
	done := make(chan CLIRun, 1)
	go func() { done <- n.RunCLI(token+"\n", "listen") }()
	return done
}

// Waited returns the listener's result, failing after timeout.
func Waited(t *testing.T, done <-chan CLIRun, timeout time.Duration, what string) CLIRun {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(timeout):
		t.Fatalf("listener did not exit: %s", what)
		return CLIRun{}
	}
}

// Silent fails if the listener exited within d.
func Silent(t *testing.T, done <-chan CLIRun, d time.Duration, what string) {
	t.Helper()
	select {
	case r := <-done:
		t.Fatalf("listener exited early (%s): %+v", what, r)
	case <-time.After(d):
	}
}

// v2 spec 7.1: the background listener wakes the idle chat with one line
// naming only the local alias and link number, for a new task and for the
// sender's task updates, and survives a daemon restart.
func TestListenerWakesForTasksAndUpdates(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	l := LinkUp(t, a, b, "tasks-auto")
	Inbox(t, l.A.C) // the link notices
	Inbox(t, l.B.C)

	bl := b.StartListener(l.B.Res.WakeToken)
	Silent(t, bl, 300*time.Millisecond, "nothing sent yet")
	var created ipc.TaskCreateResult
	Call(t, l.A.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: l.ANum, Instructions: "LISTEN-SECRET run it"}, &created)
	r := Waited(t, bl, wait, "task at bob")
	want := fmt.Sprintf("cravv-connect: 1 new task on link %d from alice. Call check_inbox.\n", l.BNum)
	if r.Code != 0 || r.Stdout != want {
		t.Fatalf("listener %+v, want %q", r, want)
	}
	if strings.Contains(r.Stdout, "LISTEN-SECRET") || strings.Contains(r.Stdout, "lead") {
		t.Fatalf("the line leaked peer text: %q", r.Stdout)
	}

	// The sender's listener wakes for the seen update once bob reads it.
	al := a.StartListener(l.A.Res.WakeToken)
	Inbox(t, l.B.C)
	r = Waited(t, al, wait, "seen update at alice")
	if want := fmt.Sprintf("cravv-connect: 1 new task update on link %d from bob. Call check_inbox.\n", l.ANum); r.Stdout != want {
		t.Fatalf("sender listener %q, want %q", r.Stdout, want)
	}
	Inbox(t, l.A.C)

	// A daemon restart under a waiting listener: it reconnects and still wakes.
	al = a.StartListener(l.A.Res.WakeToken)
	Silent(t, al, 200*time.Millisecond, "nothing new")
	a.Restart()
	a.WaitOnline()
	sa := a.Reattach("claude", l.A)
	Call(t, l.B.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: created.TaskID}, nil)
	r = Waited(t, al, 2*wait, "claimed update after the restart")
	if r.Code != 0 || !strings.Contains(r.Stdout, "1 new task update on link") {
		t.Fatalf("after restart %+v", r)
	}

	// Closing the session ends a waiting listener with a clear line.
	Inbox(t, sa.C)
	al = a.StartListener(l.A.Res.WakeToken)
	Silent(t, al, 200*time.Millisecond, "everything read")
	Call(t, sa.C, ipc.MethodSessionClose, nil, nil)
	r = Waited(t, al, wait, "session closed")
	if r.Code != 0 || !strings.Contains(r.Stdout, "session is closed") {
		t.Fatalf("closed %+v", r)
	}
}
