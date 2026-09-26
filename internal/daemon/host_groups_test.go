//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// sleeper starts `sleep 300` in a process group of its own.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	c := exec.Command("sleep", "300")
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go c.Wait() // reap it, so a killed sleeper is really gone
	t.Cleanup(func() { _ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) })
	return c
}

func writeGroup(t *testing.T, dir, runID string, g runGroup) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(g)
	if err := os.WriteFile(filepath.Join(dir, "run-"+runID+".group"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A daemon that stopped without ending its runs (a crash) kills their
// recorded process groups when it starts, but only groups of this boot.
func TestHostKillsRecordedGroupsAtStartup(t *testing.T) {
	e := newHostEnv(t, "ok", nil)
	dir := e.host.d.RunDir
	stale, otherBoot := sleeper(t), sleeper(t)
	writeGroup(t, dir, "r1", runGroup{PGID: stale.Process.Pid, Boot: bootID()})
	writeGroup(t, dir, "r2", runGroup{PGID: otherBoot.Process.Pid, Boot: "another-boot"})
	e.start(t)
	waitGone(t, stale.Process.Pid)
	Eventually(t, "the group records removed", func() bool {
		left, _ := filepath.Glob(filepath.Join(dir, "*.group"))
		return len(left) == 0
	})
	if gone(otherBoot.Process.Pid) {
		t.Fatal("a group recorded in another boot must not be killed")
	}
}

// StopAll (the kill switch, shutdown) and Close kill the run's recorded
// process group itself, even when the runner does not end it.
func TestHostStopKillsTheRecordedGroup(t *testing.T) {
	for _, how := range []string{"stop-all", "close"} {
		t.Run(how, func(t *testing.T) {
			e := newHostEnv(t, "ok", nil)
			started := make(chan *exec.Cmd, 1)
			e.runner = runnerFunc(func(ctx context.Context, c AgentCommand, env []string, d time.Duration) RunOutcome {
				p := sleeper(t)
				c.OnStart(p.Process.Pid)
				started <- p
				for !gone(p.Process.Pid) { // ignores ctx: only the host's own kill ends it
					time.Sleep(10 * time.Millisecond)
				}
				return RunOutcome{Stopped: true, ExitCode: -1}
			})
			e.start(t)
			e.task(t, "run for ever")
			p := <-started
			files, _ := filepath.Glob(filepath.Join(e.host.d.RunDir, "*.group"))
			if len(files) != 1 {
				t.Fatalf("group records while running: %v", files)
			}
			b, _ := os.ReadFile(files[0])
			if !strings.Contains(string(b), `"boot"`) {
				t.Fatalf("group record %s", b)
			}
			if how == "stop-all" {
				e.host.StopAll()
			} else if err := e.host.Close(context.Background(), e.sess.ID, "test"); err != nil {
				t.Fatal(err)
			}
			waitGone(t, p.Process.Pid)
			Eventually(t, "the group record removed", func() bool {
				left, _ := filepath.Glob(filepath.Join(e.host.d.RunDir, "*.group"))
				return len(left) == 0
			})
		})
	}
}

// A stop (kill switch, shutdown, Close) that lands between the host's
// checks and the start of the agent must still stop the run: it never
// starts.
func TestHostStopDuringStartupWindow(t *testing.T) {
	for _, how := range []string{"stop-all", "close"} {
		t.Run(how, func(t *testing.T) {
			e := newHostEnv(t, "ok", nil)
			var ran atomic.Bool
			e.runner = runnerFunc(func(ctx context.Context, c AgentCommand, env []string, d time.Duration) RunOutcome {
				ran.Store(true)
				return ExecRunner{}.Run(ctx, c, env, d)
			})
			e.host.beforeStart = func() {
				if how == "stop-all" {
					e.host.StopAll()
				} else if err := e.host.Close(context.Background(), e.sess.ID, "test"); err != nil {
					t.Error(err)
				}
			}
			e.start(t)
			tk := e.finished(t, e.task(t, "never runs"))
			if ran.Load() {
				t.Fatal("the agent started after the stop")
			}
			// Closing the session closes its link, which fails the task first.
			if want := map[string]string{"stop-all": ReasonStopped, "close": ReasonLinkClosed}[how]; tk.State != core.TaskFailed || lastNote(tk) != want {
				t.Fatalf("task %+v, want failed %s", tk, want)
			}
			Eventually(t, "the stopped run audited", func() bool {
				ev := e.audit.ofType(EvManagedRun)
				return len(ev) == 1 && ev[0].Detail["outcome"] == "stopped"
			})
		})
	}
}
