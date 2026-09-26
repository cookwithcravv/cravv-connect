package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// versionedDaemon is a fake daemon whose status reports a version that a
// restart changes to this binary's, as a daemon started from the new
// binary does.
type versionedDaemon struct {
	fd *fakeDaemon
	mu sync.Mutex
	v  string
}

func newVersionedDaemon(t *testing.T, running string) *versionedDaemon {
	vd := &versionedDaemon{fd: newFakeDaemon(t), v: running}
	vd.fd.handle(ipc.MethodStatus, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		vd.mu.Lock()
		defer vd.mu.Unlock()
		return ipc.StatusResult{Version: vd.v, RelayURL: "https://relay.example.com", RelayConnected: true}, nil
	})
	return vd
}

// upgrade makes the next status report this binary's version.
func (vd *versionedDaemon) upgrade() {
	vd.mu.Lock()
	defer vd.mu.Unlock()
	vd.v = BuildVersion()
}

// upgradingService is the login service around a versioned daemon: Start
// serves the new binary.
type upgradingService struct {
	*daemonProcess
	vd *versionedDaemon
}

func (s upgradingService) Start(ctx context.Context) error {
	s.vd.upgrade()
	return s.daemonProcess.Start(ctx)
}

// daemon start restarts a running daemon of another version through the
// login service; a v1 daemon reports no version and counts as another.
func TestDaemonStartRestartsAnotherVersion(t *testing.T) {
	for _, tc := range []struct{ running, was string }{{"v1.9.0", "v1.9.0"}, {"", "unknown"}} {
		vd := newVersionedDaemon(t, tc.running)
		proc := &daemonProcess{fd: vd.fd}
		proc.up()
		env, out, errb := vd.fd.env(&fakePrompter{}, "")
		env.Service = upgradingService{proc, vd}
		if code := Main([]string{"daemon", "start"}, env); code != 0 {
			t.Fatalf("%q: exit %d: %s", tc.running, code, errb.String())
		}
		if want := "Restarted the daemon (was " + tc.was + ", now " + BuildVersion() + ").\n"; out.String() != want {
			t.Fatalf("%q: stdout %q, want %q", tc.running, out.String(), want)
		}
		if !slices.Equal(proc.events, []string{"stop", "start"}) {
			t.Fatalf("%q: service %v", tc.running, proc.events)
		}
	}
}

// Without a login service the old daemon is stopped over its socket and the
// new one spawned.
func TestDaemonStartRestartsAnotherVersionWithoutService(t *testing.T) {
	vd := newVersionedDaemon(t, "v1.9.0")
	var stop func()
	vd.fd.handle(ipc.MethodDaemonShutdown, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		go stop()
		return nil, nil
	})
	stop = vd.fd.serve()
	env, out, errb := vd.fd.env(&fakePrompter{}, "")
	env.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }
	var spawned []string
	env.Spawn = func(exe string, args []string, _ string) (int, error) {
		spawned = append([]string{exe}, args...)
		vd.upgrade()
		vd.fd.serve()
		return 4242, nil
	}
	if code := Main([]string{"daemon", "start"}, env); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if want := "Restarted the daemon (was v1.9.0, now " + BuildVersion() + ").\n"; out.String() != want {
		t.Fatalf("stdout %q, want %q", out.String(), want)
	}
	if len(spawned) < 3 || spawned[1] != "daemon" || spawned[2] != "run" {
		t.Fatalf("spawned %v", spawned)
	}
	if !slices.Contains(vd.fd.methods(), ipc.MethodDaemonShutdown) {
		t.Fatalf("calls %v", vd.fd.methods())
	}
}

// A daemon of this version is left alone.
func TestDaemonStartKeepsSameVersion(t *testing.T) {
	vd := newVersionedDaemon(t, BuildVersion())
	proc := &daemonProcess{fd: vd.fd}
	proc.up()
	env, out, _ := vd.fd.env(&fakePrompter{}, "")
	env.Service = upgradingService{proc, vd}
	if code := Main([]string{"daemon", "start"}, env); code != 0 || out.String() != "Daemon is already running.\n" || len(proc.events) != 0 {
		t.Fatalf("exit %d stdout %q service %v", code, out.String(), proc.events)
	}
}

// setup restarts a running daemon of another version before going on.
func TestSetupRestartsAnotherVersion(t *testing.T) {
	vd := newVersionedDaemon(t, "")
	r := newSetupRigOn(t, vd.fd)
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	r.env.Service = upgradingService{r.daemon, vd}
	r.prompt.lines = []string{"n"}
	if code := r.run("--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if want := "== Daemon ==\nRestarted the daemon (was unknown, now " + BuildVersion() + ").\nConnected to the relay"; !strings.Contains(r.out.String(), want) {
		t.Fatalf("stdout lacks %q:\n%s", want, r.out.String())
	}
	if !slices.Equal(r.daemon.events, []string{"stop", "start"}) {
		t.Fatalf("service %v", r.daemon.events)
	}
}
