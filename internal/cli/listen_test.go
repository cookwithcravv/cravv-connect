package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

const testWake = "wake-token-abc"

func listenDaemon(t *testing.T, res ipc.ListenResult, err error) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodSessionListen, ipc.GateNone, func(_ *ipc.ConnState, raw json.RawMessage) (any, error) {
		var p ipc.SessionListenParams
		json.Unmarshal(raw, &p)
		if p.WakeToken != testWake || p.TimeoutS != 0 {
			return nil, core.ErrNotFound
		}
		return res, err
	})
	fd.start()
	return fd
}

func TestListenPrintsOneLineWithAliasAndLink(t *testing.T) {
	fd := listenDaemon(t, ipc.ListenResult{Unread: 1, Pending: []ipc.PendingCount{{Link: 2, Machine: "gpu-box", Kind: "task", Count: 1}}}, nil)
	r := fd.runStdin(nil, testWake+"\n", "listen")
	if r.code != 0 || r.stdout != "cravv-connect: 1 new task on link 2 from gpu-box. Call check_inbox.\n" || r.stderr != "" {
		t.Fatalf("code %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if strings.Contains(r.stdout, testWake) {
		t.Fatal("the token was printed")
	}
}

func TestListenClosedAndInvalid(t *testing.T) {
	fd := listenDaemon(t, ipc.ListenResult{Closed: true}, nil)
	if r := fd.runStdin(nil, testWake, "listen"); r.code != 0 || r.stdout != listenClosedLine+"\n" {
		t.Fatalf("closed: %d %q", r.code, r.stdout)
	}
	if r := fd.runStdin(nil, "other-token", "listen"); r.code != 1 || r.stdout != listenInvalidLine+"\n" || r.stderr != "" {
		t.Fatalf("invalid: %d %q %q", r.code, r.stdout, r.stderr)
	}
	if r := fd.runStdin(nil, "", "listen"); r.code != 1 || !strings.Contains(r.stdout, "no wake token") {
		t.Fatalf("no token: %d %q", r.code, r.stdout)
	}
	if r := fd.runStdin(nil, testWake, "listen", testWake); r.code != 1 || !strings.Contains(r.stderr, "unknown command") && !strings.Contains(r.stderr, "accepts 0 arg") {
		t.Fatalf("a token as an argument must be refused: %d %q %q", r.code, r.stdout, r.stderr)
	}
}

func TestListenWakeFileMustBePrivate(t *testing.T) {
	fd := listenDaemon(t, ipc.ListenResult{Unread: 1, Pending: []ipc.PendingCount{{Link: 1, Machine: "mac", Kind: "message", Count: 1}}}, nil)
	dir := t.TempDir()
	good := filepath.Join(dir, "wake")
	if err := os.WriteFile(good, []byte(testWake+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := fd.run(nil, "listen", "--wake-file", good); r.code != 0 || !strings.Contains(r.stdout, "1 new message on link 1 from mac") {
		t.Fatalf("private file: %d %q", r.code, r.stdout)
	}
	open := filepath.Join(dir, "open")
	if err := os.WriteFile(open, []byte(testWake), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := fd.run(nil, "listen", "--wake-file", open); r.code != 1 || !strings.Contains(r.stdout, "must be 0600") {
		t.Fatalf("readable file: %d %q", r.code, r.stdout)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if r := fd.run(nil, "listen", "--wake-file", link); r.code != 1 || !strings.Contains(r.stdout, "is a symbolic link") {
		t.Fatalf("symlink: %d %q", r.code, r.stdout)
	}
}

// Review focus: the wake file is checked on the descriptor that is read
// (opened with O_NOFOLLOW, then fstat), so swapping the path for a symlink
// or a FIFO between a check and the open does not work.
func TestOpenWakeFileChecksTheOpenedFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "wake")
	if err := os.WriteFile(good, []byte(testWake), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := openWakeFile(good)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	link := filepath.Join(dir, "link")
	os.Symlink(good, link)
	if _, err := openWakeFile(link); err == nil || !strings.Contains(err.Error(), "is a symbolic link") {
		t.Fatalf("symlink: %v", err)
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		f, err := openWakeFile(fifo)
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("fifo: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("opening a FIFO blocked")
	}
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o700)
	if _, err := openWakeFile(sub); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("directory: %v", err)
	}
	loose := filepath.Join(dir, "loose")
	os.WriteFile(loose, []byte(testWake), 0o600)
	os.Chmod(loose, 0o640)
	if _, err := openWakeFile(loose); err == nil || !strings.Contains(err.Error(), "must be 0600") {
		t.Fatalf("group-readable: %v", err)
	}
}

// scriptCaller answers session.listen from a script, one entry per call.
type scriptCaller struct {
	mu     *sync.Mutex
	script *[]func(result any) error
}

func (s scriptCaller) Call(_ context.Context, _ string, _, result any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := (*s.script)[0]
	*s.script = (*s.script)[1:]
	return next(result)
}

func (scriptCaller) Close() error { return nil }

// The daemon restarts under a listener: the call drops, dials fail for a
// while, then the listener reconnects and still wakes. With no daemon at
// all it gives up after listenMaxFailures tries.
func TestListenSurvivesADaemonRestart(t *testing.T) {
	old := [3]any{listenBackoffMin, listenBackoffMax, listenMaxFailures}
	listenBackoffMin, listenBackoffMax, listenMaxFailures = time.Millisecond, 4*time.Millisecond, 5
	t.Cleanup(func() {
		listenBackoffMin, listenBackoffMax, listenMaxFailures = old[0].(time.Duration), old[1].(time.Duration), old[2].(int)
	})
	fd := newFakeDaemon(t)
	env, out, _ := fd.env(&fakePrompter{}, testWake)
	var mu sync.Mutex
	script := []func(any) error{
		func(any) error { return ipc.ErrClosed },                  // the daemon stopped mid-listen
		func(any) error { return errors.New("context canceled") }, // or answered as it stopped
		func(r any) error {
			*r.(*ipc.ListenResult) = ipc.ListenResult{Unread: 2, Pending: []ipc.PendingCount{{Link: 3, Machine: "gpu-box", Kind: "message", Count: 2}}}
			return nil
		},
	}
	dials := 0
	env.Dial = func(context.Context) (Caller, error) {
		dials++
		if dials == 3 || dials == 4 { // down while it restarts
			return nil, ipc.ErrDaemonNotRunning
		}
		return scriptCaller{&mu, &script}, nil
	}
	if code := Main([]string{"listen"}, env); code != 0 || out.String() != "cravv-connect: 2 new messages on link 3 from gpu-box. Call check_inbox.\n" {
		t.Fatalf("code %d out %q", code, out.String())
	}
	if dials != 5 {
		t.Fatalf("dials %d", dials)
	}

	env, out, _ = fd.env(&fakePrompter{}, testWake)
	env.Dial = func(context.Context) (Caller, error) { return nil, ipc.ErrDaemonNotRunning }
	if code := Main([]string{"listen"}, env); code != 1 || out.String() != listenLostLine+"\n" {
		t.Fatalf("no daemon: code %d out %q", code, out.String())
	}
}
