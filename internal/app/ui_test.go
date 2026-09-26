package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

// uiDaemon serves a real daemon with the UI methods over in-process pipes.
func uiDaemon(t *testing.T) (dir string, dial func() *ipc.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "appui")
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
		Paths: paths, Config: config.Config{DeviceName: "test-mac"}, Verifier: auth.Fake{Password: "pw"}, Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore { return daemon.SettingsIdentityStore{Settings: s} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	srv := api.NewServer(Ports(d), core.SystemClock{}, nil)
	api.RegisterUI(srv, UIPorts(d, nil))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return dir, func() *ipc.Client {
		c, _ := srv.Pipe(ctx)
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func TestUIPortsAgainstRealDaemon(t *testing.T) {
	dir, dial := uiDaemon(t)
	ctx := context.Background()
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(proj, 0o700)
	share := func(name, vis string) *ipc.Client {
		c := dial()
		if err := c.Call(ctx, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: proj}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Call(ctx, ipc.MethodSessionShare, ipc.SessionShareParams{Name: name, Purpose: name + " work", Visibility: vis}, nil); err != nil {
			t.Fatal(err)
		}
		return c
	}
	share("lead", "all-peers")
	share("gone", "private").Call(ctx, ipc.MethodSessionClose, nil, nil)

	human := dial()
	var r ipc.LocalSessionsResult
	if err := human.Call(ctx, ipc.MethodSessionsLocal, nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 1 || r.Sessions[0].Name != "lead" || r.Sessions[0].Visibility != "all-peers" || r.Sessions[0].Purpose != "lead work" {
		t.Fatalf("local sessions %+v", r.Sessions)
	}
	if err := human.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "pw"}, nil); err != nil {
		t.Fatal(err)
	}
	err := human.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "gone", Target: "bob/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), `no open session "gone"`) {
		t.Fatalf("closed session: %v", err)
	}
	// An open session gets as far as the target machine, which is not paired.
	err = human.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "lead", Target: "bob/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), `peer "bob"`) {
		t.Fatalf("open session: %v", err)
	}
}
