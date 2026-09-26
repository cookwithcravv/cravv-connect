package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// Serve starts the web UI on ui.start and stops it before it returns.
func TestServeStopsTheWebUI(t *testing.T) {
	dir, err := os.MkdirTemp("", "serveui")
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
		Paths: paths, Config: config.Config{DeviceName: "serve-ui"}, Verifier: auth.Fake{Password: "pw"}, Username: "tester",
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

	var r ipc.UIStartResult
	if err := dial(t, paths.Socket).Call(ctx, ipc.MethodUIStart, nil, &r); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 1 {
		t.Fatalf("launch: %d, cookies %v", res.StatusCode, res.Cookies())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	if c, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
		c.Close()
		t.Fatal("the web UI still accepts connections after Serve returned")
	}
}
