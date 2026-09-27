package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/auth"
	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/daemon"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

func TestServeRunsDaemonAndAPIUntilCancelled(t *testing.T) {
	dir, err := os.MkdirTemp("", "serve")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := config.Paths{
		Home: dir, Config: filepath.Join(dir, "config.toml"), DB: filepath.Join(dir, "store.db"),
		Audit: filepath.Join(dir, "audit.log"), Socket: filepath.Join(dir, "d.sock"),
		Files: filepath.Join(dir, "files"), Log: filepath.Join(dir, "daemon.log"),
	}
	d, err := daemon.New(daemon.Options{
		Paths:         paths,
		Config:        config.Config{DeviceName: "serve-test"},
		Verifier:      auth.Fake{Password: "pw"},
		Username:      "tester",
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
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, d, ln, core.SystemClock{}, nil) }()

	c := dial(t, paths.Socket)
	var st ipc.StatusResult
	if err := c.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
		t.Fatal(err)
	}
	if st.MachineID != string(d.Identity().MachineID()) || st.DeviceName != "serve-test" {
		t.Fatalf("status %+v", st)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v after cancel, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if _, err := ipc.Dial(paths.Socket); err == nil {
		t.Fatal("socket still accepting after Serve returned")
	}
}
