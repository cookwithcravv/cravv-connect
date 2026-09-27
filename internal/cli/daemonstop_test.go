package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// stoppable starts fd and returns a function that stops serving (removing
// the socket), as a real daemon does after daemon.shutdown.
func (fd *fakeDaemon) startStoppable() func() {
	ln, err := ipc.Listen(fd.sock)
	if err != nil {
		fd.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { fd.srv.Serve(ctx, ln); close(done) }()
	fd.t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestDaemonStopUsesShutdownAndWaitsForSocket(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{})
	var stop func()
	fd.handle(ipc.MethodDaemonShutdown, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		time.AfterFunc(200*time.Millisecond, stop)
		return nil, nil
	})
	stop = fd.startStoppable()
	// A pid file naming this test process must never be signalled.
	os.WriteFile(filepath.Join(fd.home, "daemon.pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	r := fd.run(nil, "daemon", "stop")
	if r.code != 0 || r.stdout != "Daemon stopped.\n" {
		t.Fatalf("%d %q %q", r.code, r.stdout, r.stderr)
	}
	if got := strings.Join(fd.methods(), ","); got != "status,daemon.shutdown" {
		t.Fatalf("calls %s", got)
	}
	if _, err := os.Stat(fd.sock); !os.IsNotExist(err) {
		t.Fatalf("stop returned before the socket was gone: %v", err)
	}
}

func TestDaemonStopTimesOut(t *testing.T) {
	old := stopWait
	stopWait = 300 * time.Millisecond
	t.Cleanup(func() { stopWait = old })
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{})
	fd.reply(ipc.MethodDaemonShutdown, ipc.GateAllowWhenKilled, nil) // says yes, never stops
	fd.start()
	r := fd.run(nil, "daemon", "stop")
	if r.code == 0 || !strings.Contains(r.stderr, "did not stop") {
		t.Fatalf("%d %q %q", r.code, r.stdout, r.stderr)
	}
}

func TestDaemonStopFallsBackToPIDWhenShutdownUnsupported(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{})
	fd.start() // an older daemon: no daemon.shutdown method
	proc := exec.Command("sleep", "30")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { proc.Wait(); close(exited) }()
	t.Cleanup(func() { proc.Process.Kill() })
	os.WriteFile(filepath.Join(fd.home, "daemon.pid"), []byte(strconv.Itoa(proc.Process.Pid)), 0o600)
	r := fd.run(nil, "daemon", "stop")
	if r.code != 0 || r.stdout != "Daemon stopped.\n" {
		t.Fatalf("%d %q %q", r.code, r.stdout, r.stderr)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("pid fallback did not signal the process")
	}
	if proc.ProcessState == nil || proc.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Fatalf("process state %v", proc.ProcessState)
	}
}

func TestDaemonStopWithoutSocketSignalsNothing(t *testing.T) {
	fd := newFakeDaemon(t)
	proc := exec.Command("sleep", "30")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Process.Kill(); proc.Wait() })
	os.WriteFile(filepath.Join(fd.home, "daemon.pid"), []byte(strconv.Itoa(proc.Process.Pid)), 0o600)
	if r := fd.run(nil, "daemon", "stop"); r.stdout != "Daemon is not running.\n" {
		t.Fatalf("%q %q", r.stdout, r.stderr)
	}
	if err := proc.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("a process named only by the pid file was signalled: %v", err)
	}
}
