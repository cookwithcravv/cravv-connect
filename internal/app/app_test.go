package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestPortsFillsEveryPort(t *testing.T) {
	p := Ports(nil)
	v := reflect.ValueOf(p)
	for i := range v.NumField() {
		if v.Type().Field(i).Name == "Lifecycle" {
			continue // optional: Serve sets it
		}
		if v.Field(i).IsNil() {
			t.Errorf("port %s is nil", v.Type().Field(i).Name)
		}
	}
}

func TestDaemonErrorsHaveKinds(t *testing.T) {
	cases := map[error]string{
		daemon.ErrBadAlias:          ipc.KindBadRequest,
		daemon.ErrOffline:           KindOffline,
		daemon.ErrOfflineForPairing: KindOffline,
		daemon.ErrPairingFailed:     KindPairingFailed,
		daemon.ErrPairingExpired:    KindPairingExpired,
		daemon.ErrPairingInProgress: KindBusy,
		daemon.ErrPairingClosed:     KindOffline,
		auth.ErrUnavailable:         KindAuthUnavailable,
		auth.ErrServiceNotAllowed:   KindAuthUnavailable,
		auth.ErrAcceptsAnyPassword:  KindAuthUnavailable,
	}
	for err, kind := range cases {
		if got := ipc.KindOf(fmt.Errorf("wrapped: %w", err)); got != kind {
			t.Errorf("%v: kind %q, want %q", err, got, kind)
		}
	}
}

// realDaemon builds a daemon with no relay, the fake password "pw", and the
// identity kept in the store, then serves the API on a short temp socket.
func realDaemon(t *testing.T) (sock, dir string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "app")
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
		Config:        config.Config{DeviceName: "test-mac"},
		Verifier:      auth.Fake{Password: "pw"},
		Username:      "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore { return daemon.SettingsIdentityStore{Settings: s} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ln, err := ipc.Listen(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := api.NewServer(Ports(d), core.SystemClock{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return paths.Socket, dir
}

func dial(t *testing.T, sock string) *ipc.Client {
	t.Helper()
	c, err := ipc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAgainstRealDaemon(t *testing.T) {
	sock, dir := realDaemon(t)
	ctx := context.Background()
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(proj, 0o700)

	a, b := dial(t, sock), dial(t, sock)
	var ra, rb ipc.SessionRegisterResult
	if err := a.Call(ctx, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: proj}, &ra); err != nil {
		t.Fatal(err)
	}
	if err := b.Call(ctx, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: proj}, &rb); err != nil {
		t.Fatal(err)
	}
	if ra.Name != "claude@proj" || rb.Name != "claude@proj-2" {
		t.Fatalf("names %q %q", ra.Name, rb.Name)
	}
	var sh ipc.ShareResult
	if err := a.Call(ctx, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "lead", Visibility: "all-peers"}, &sh); err != nil {
		t.Fatal(err)
	}
	if sh.WakeToken == "" || sh.ReattachToken == "" || sh.Session.Name != "lead" || sh.Session.Visibility != "all-peers" || sh.Session.Agent != "claude" {
		t.Fatalf("share %+v", sh)
	}
	if err := b.Call(ctx, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "second"}, nil); err != nil {
		t.Fatal(err)
	}

	var st ipc.StatusResult
	if err := a.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.MachineID) != 52 || st.DeviceName != "test-mac" || st.RelayConnected || !slices.Contains(st.Sessions, "lead (open)") {
		t.Fatalf("status %+v", st)
	}
	var pl ipc.PeerListResult
	if err := a.Call(ctx, ipc.MethodPeerList, nil, &pl); err != nil || pl.Peers == nil || len(pl.Peers) != 0 {
		t.Fatalf("peers %v %+v", err, pl)
	}
	var in ipc.InboxResult
	if err := a.Call(ctx, ipc.MethodInboxCheck, ipc.InboxCheckParams{}, &in); err != nil || in.Items == nil || len(in.Items) != 0 {
		t.Fatalf("inbox %v %+v", err, in)
	}
	var hc ipc.HookCountsResult
	if err := a.Call(ctx, ipc.MethodHookCounts, ipc.HookCountsParams{Cwd: proj}, &hc); err != nil || hc.Notice != "" {
		t.Fatalf("hook %v %+v", err, hc)
	}
	if err := a.Call(ctx, ipc.MethodChatSend, ipc.ChatSendParams{Link: 99, Text: "hi"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("chat on an unknown link: %v", err)
	}

	if err := a.Call(ctx, ipc.MethodAllowPathAdd, ipc.AllowPathParams{Path: proj}, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("allow-path without unlock: %v", err)
	}
	if err := a.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "nope"}, nil); !errors.Is(err, core.ErrBadPassword) {
		t.Fatalf("bad password: %v", err)
	}
	if err := a.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "pw"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Call(ctx, ipc.MethodAllowPathAdd, ipc.AllowPathParams{Path: proj}, nil); err != nil {
		t.Fatalf("allow-path: %v", err)
	}
	if err := a.Call(ctx, ipc.MethodAllowPathAdd, ipc.AllowPathParams{Path: filepath.Join(dir, "missing")}, nil); err == nil {
		t.Fatal("allow-path accepted a missing directory")
	}

	if err := b.Call(ctx, ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Call(ctx, ipc.MethodInboxCheck, nil, nil); !errors.Is(err, core.ErrKilled) {
		t.Fatalf("inbox while killed: %v", err)
	}
	// The daemon's outbound queue accepts envelopes while killed, so the IPC
	// kill gate must refuse every send.
	for m, params := range map[string]any{
		ipc.MethodChatSend:   ipc.ChatSendParams{Link: 1, Text: "hi"},
		ipc.MethodTaskCreate: ipc.TaskCreateParams{Link: 1, Instructions: "go"},
		ipc.MethodFileSend:   ipc.FileSendParams{Link: 1, Path: "a.txt"},
	} {
		if err := a.Call(ctx, m, params, nil); !ipc.IsKind(err, ipc.KindKilled) {
			t.Fatalf("%s while killed: %v", m, err)
		}
	}
	if err := b.Call(ctx, ipc.MethodResume, nil, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("resume without unlock: %v", err)
	}
	if err := a.Call(ctx, ipc.MethodResume, nil, nil); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if err := b.Call(ctx, ipc.MethodInboxCheck, nil, nil); err != nil {
		t.Fatalf("inbox after resume: %v", err)
	}
}

// reset_identity is a recovery step for a compromised machine: it must work
// while the kill switch is on (it needs the password, not a resume first).
func TestResetIdentityWhileKilled(t *testing.T) {
	sock, _ := realDaemon(t)
	ctx := context.Background()
	c := dial(t, sock)
	var before ipc.StatusResult
	if err := c.Call(ctx, ipc.MethodStatus, nil, &before); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, ipc.MethodResetIdentity, nil, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("reset_identity while killed without unlock: %v", err)
	}
	if err := c.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "pw"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, ipc.MethodResetIdentity, nil, nil); err != nil {
		t.Fatalf("reset_identity while killed: %v", err)
	}
	var after ipc.StatusResult
	if err := c.Call(ctx, ipc.MethodStatus, nil, &after); err != nil {
		t.Fatal(err)
	}
	if after.MachineID == before.MachineID || !after.Killed {
		t.Fatalf("after reset: machine %s (was %s), killed %v", after.MachineID, before.MachineID, after.Killed)
	}
}
