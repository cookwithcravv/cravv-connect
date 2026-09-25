package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

func shortPaths(t *testing.T) config.Paths {
	t.Helper()
	dir, err := os.MkdirTemp("", "life")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return config.Paths{
		Home: dir, Config: filepath.Join(dir, "config.toml"), DB: filepath.Join(dir, "store.db"),
		Audit: filepath.Join(dir, "audit.log"), Socket: filepath.Join(dir, "d.sock"),
		Files: filepath.Join(dir, "files"), Log: filepath.Join(dir, "daemon.log"),
	}
}

func TestListenWritesPIDOnlyAfterListening(t *testing.T) {
	paths := shortPaths(t)
	// Another daemon owns the socket: no pid file may be written.
	other, err := ipc.Listen(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := other.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, _, err := listenWithPID(paths); !errors.Is(err, ipc.ErrAlreadyRunning) {
		t.Fatalf("err = %v, want ErrAlreadyRunning", err)
	}
	if _, err := os.Stat(paths.PIDFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file written although listening failed: %v", err)
	}
	other.Close()

	ln, cleanup, err := listenWithPID(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	b, err := os.ReadFile(paths.PIDFile())
	if err != nil || string(b) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid file = %q, %v", b, err)
	}
	cleanup()
	if _, err := os.Stat(paths.PIDFile()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pid file not removed")
	}
}

func TestPIDCleanupLeavesSomeoneElsesFile(t *testing.T) {
	paths := shortPaths(t)
	ln, cleanup, err := listenWithPID(paths)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	// A later daemon took over the pid file.
	os.WriteFile(paths.PIDFile(), []byte("12345"), 0o600)
	cleanup()
	if b, _ := os.ReadFile(paths.PIDFile()); string(b) != "12345" {
		t.Fatalf("cleanup removed or changed another daemon's pid file: %q", b)
	}
}

func TestDaemonShutdownStopsServe(t *testing.T) {
	paths := shortPaths(t)
	d, err := daemon.New(daemon.Options{
		Paths: paths, Config: config.Config{DeviceName: "x"}, Verifier: auth.Fake{Password: "pw"}, Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore { return daemon.SettingsIdentityStore{Settings: s} },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ln, err := ipc.Listen(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), d, ln, core.SystemClock{}, nil) }()
	c := dial(t, paths.Socket)
	// Allowed while killed and without a password, like kill itself.
	if err := c.Call(context.Background(), ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(context.Background(), ipc.MethodDaemonShutdown, nil, nil); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve still running after daemon.shutdown")
	}
	if _, err := os.Stat(paths.Socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present: %v", err)
	}
}
