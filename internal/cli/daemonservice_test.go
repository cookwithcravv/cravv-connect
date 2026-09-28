package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/config"
)

// A start that times out shows the end of the daemon's stderr log, where a
// daemon that failed before logging leaves its reason.
func TestDaemonStartTimeoutShowsStderrTail(t *testing.T) {
	old := startWait
	startWait = 300 * time.Millisecond
	t.Cleanup(func() { startWait = old })
	fd := newFakeDaemon(t)
	fd.setUp(t)
	var log strings.Builder
	for i := 1; i <= 15; i++ {
		fmt.Fprintf(&log, "line %d\n", i)
	}
	os.WriteFile(filepath.Join(fd.home, "daemon-stderr.log"), []byte(log.String()+"\x1b[31mred\n"), 0o600)
	env, _, errb := fd.env(&fakePrompter{}, "")
	env.Service = &fakeService{} // starts nothing
	if code := Main([]string{"daemon", "start"}, env); code == 0 {
		t.Fatal("start succeeded without a daemon")
	}
	msg := errb.String()
	if !strings.Contains(msg, "did not start within") || !strings.Contains(msg, "line 15") || !strings.Contains(msg, "line 7") ||
		strings.Contains(msg, "line 6\n") || strings.Contains(msg, "\x1b") {
		t.Fatalf("stderr = %q", msg)
	}
	if startWait = old; startWait < 15*time.Second {
		t.Fatalf("default start wait %v, want at least 15s", startWait)
	}
}

// After the login service stops the daemon, stop waits for the old process
// to exit, so a start right after never races it.
func TestDaemonStopWithServiceWaitsForTheProcess(t *testing.T) {
	fd := newFakeDaemon(t)
	cmd := exec.Command("sleep", "0.4")
	if err := cmd.Start(); err != nil {
		t.Skipf("no sleep: %v", err)
	}
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	os.WriteFile(filepath.Join(fd.home, "daemon.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	env, out, errb := fd.env(&fakePrompter{}, "")
	svc := &fakeService{}
	env.Service = svc
	start := time.Now()
	if code := Main([]string{"daemon", "stop"}, env); code != 0 || !svc.stopped {
		t.Fatalf("exit %d %q %q", code, out.String(), errb.String())
	}
	select {
	case <-exited:
	default:
		t.Fatalf("stop returned after %v while the old daemon process still ran", time.Since(start))
	}
}

// setUp writes a config with a relay, like `cravv-connect setup` does.
func (fd *fakeDaemon) setUp(t *testing.T) {
	t.Helper()
	p := config.Paths{Home: fd.home, Config: filepath.Join(fd.home, "config.toml")}
	c := config.Defaults()
	c.RelayURL = "https://relay.test"
	if err := config.Save(p, c); err != nil {
		t.Fatal(err)
	}
}
