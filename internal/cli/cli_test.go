package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

var paired = time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)

func TestDaemonNotRunning(t *testing.T) {
	fd := newFakeDaemon(t) // never started
	r := fd.run(nil, "peers")
	if r.code != 1 || r.stderr != "error: daemon not running: run `cravv-connect daemon start`\n" {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
}

func TestPeersTable(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodPeerList, ipc.GateAllowWhenKilled, ipc.PeerListResult{Peers: []ipc.PeerView{
		{Alias: "gpu-box", MachineID: "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst", Online: true, PairedAt: paired},
		{Alias: "mac", MachineID: "m2", PausedByPeer: true, PairedAt: paired},
	}})
	fd.start()
	r := fd.run(nil, "peers")
	want := "" +
		"ALIAS    STATE           MACHINE ID                                            PAIRED\n" +
		"gpu-box  online          abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst  2026-09-26 10:00 UTC\n" +
		"mac      paused by peer  m2                                                    2026-09-26 10:00 UTC\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("code %d\n%s\nwant\n%s", r.code, r.stdout, want)
	}
}

func TestPeersEmpty(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodPeerList, ipc.GateNone, ipc.PeerListResult{})
	fd.start()
	if r := fd.run(nil, "peers"); r.stdout != "No peers yet. Run `cravv-connect pair` to add one.\n" {
		t.Fatalf("%q", r.stdout)
	}
}

func TestPairFlow(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodPairStart, ipc.GateUnlock, ipc.PairStartResult{PendingID: "P1", Code: "CRAVV-7K3F-9QXM-TR2A"})
	fd.reply(ipc.MethodPairAwait, ipc.GateUnlock, ipc.PendingPeerResult{PendingID: "P1", SuggestedName: "GPU Box!!", MachineID: "abcdefghijklmnopqrstuvwxyz"})
	fd.reply(ipc.MethodPairFinalize, ipc.GateUnlock, ipc.PairFinalizeResult{Alias: "gpu-box"})
	fd.start()
	p := &fakePrompter{passwords: []string{"wrong", "pw"}, lines: []string{""}}
	r := fd.run(p, "pair")
	if r.code != 0 {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	want := "" +
		"Bind code: CRAVV-7K3F-9QXM-TR2A\n\n" +
		"On the other machine run:\n" +
		"  cravv-connect join CRAVV-7K3F-9QXM-TR2A\n" +
		"The code works once and expires in 10 minutes.\n" +
		"Waiting for the other machine...\n" +
		"Connected to machine abcdefghijklmnop.\n" +
		"Paired with gpu-box. Its sessions can now ask to link with yours; you decide each link.\n" +
		"Machine ID: abcdefghijklmnopqrstuvwxyz\n"
	if r.stdout != want {
		t.Fatalf("stdout\n%s\nwant\n%s", r.stdout, want)
	}
	if !strings.Contains(r.stderr, "Incorrect password, try again.") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if got := fd.params(ipc.MethodPairFinalize); got != `{"pending_id":"P1","alias":"gpu-box"}` {
		t.Fatalf("finalize params %s", got)
	}
	// The password was asked once for the whole flow (one unlocked connection).
	if n := strings.Count(strings.Join(r.prompt.asked, "\n"), "password:"); n != 2 {
		t.Fatalf("password prompts %d: %v", n, r.prompt.asked)
	}
}

func TestJoinPassesCode(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodJoinStart, ipc.GateUnlock, ipc.PendingPeerResult{PendingID: "P2", SuggestedName: "mac", MachineID: "m"})
	fd.reply(ipc.MethodPairFinalize, ipc.GateUnlock, ipc.PairFinalizeResult{Alias: "laptop"})
	fd.start()
	p := &fakePrompter{passwords: []string{"pw"}, lines: []string{"laptop"}}
	r := fd.run(p, "join", "cravv-7k3f-9qxm-tr2a")
	if r.code != 0 || !strings.Contains(r.stdout, "Paired with laptop.") {
		t.Fatalf("%d %s %s", r.code, r.stdout, r.stderr)
	}
	if fd.params(ipc.MethodJoinStart) != `{"code":"cravv-7k3f-9qxm-tr2a"}` {
		t.Fatalf("join params %s", fd.params(ipc.MethodJoinStart))
	}
}

func TestLockedStopsRetrying(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodResume, ipc.GateUnlock|ipc.GateAllowWhenKilled, nil)
	fd.start()
	p := &fakePrompter{passwords: []string{"a", "b", "c", "d"}}
	r := fd.run(p, "resume")
	if r.code != 1 || !strings.Contains(r.stderr, "incorrect password") {
		t.Fatalf("%d %q", r.code, r.stderr)
	}
	if len(p.passwords) != 1 {
		t.Fatalf("asked %d times, want 3", 4-len(p.passwords))
	}
}

func TestUnpairConfirms(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodPeerUnpair, ipc.GateNone, nil)
	fd.start()
	r := fd.run(&fakePrompter{lines: []string{"no"}}, "unpair", "gpu-box")
	if r.stdout != "Nothing changed.\n" || slices.Contains(fd.methods(), ipc.MethodPeerUnpair) {
		t.Fatalf("declined: %q %v", r.stdout, fd.methods())
	}
	r = fd.run(&fakePrompter{lines: []string{"yes"}}, "unpair", "gpu-box")
	if r.stdout != "Unpaired gpu-box.\n" {
		t.Fatalf("confirmed: %q", r.stdout)
	}
	r = fd.run(nil, "unpair", "--yes", "gpu-box")
	if r.stdout != "Unpaired gpu-box.\n" {
		t.Fatalf("--yes: %q", r.stdout)
	}
}

func TestPauseKillResume(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodPeerPause, ipc.GateNone, nil)
	fd.reply(ipc.MethodKill, ipc.GateNone, nil)
	fd.reply(ipc.MethodResume, ipc.GateUnlock|ipc.GateAllowWhenKilled, nil)
	fd.start()
	if r := fd.run(nil, "pause", "gpu-box"); r.stdout != "Paused gpu-box.\n" {
		t.Fatalf("%q", r.stdout)
	}
	if r := fd.run(nil, "kill"); r.stdout != "Kill switch is on. All traffic stopped.\nRun `cravv-connect resume` to turn it off (asks for your password).\n" {
		t.Fatalf("%q", r.stdout)
	}
	if r := fd.run(&fakePrompter{passwords: []string{"pw"}}, "resume"); r.stdout != "Kill switch is off. Traffic resumed.\n" {
		t.Fatalf("%q %q", r.stdout, r.stderr)
	}
}

func TestApprovalsInteractive(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodApprovalsList, ipc.GateUnlock, ipc.ApprovalsListResult{Tasks: []ipc.ApprovalView{
		{TaskID: "T1", Peer: "gpu-box", Preview: "run \x1b[2Jtests", Full: "run \x1b[2Jtests fully", SHA256: "ab12", Size: 20, Received: paired},
		{TaskID: "T2", Peer: "mac", Preview: "rm -rf", Full: "rm -rf", SHA256: "cd34", Size: 6, Received: paired},
		{TaskID: "T3", Peer: "mac", Preview: "later", Full: "later", SHA256: "ef56", Size: 5, Received: paired},
	}})
	var decided []string
	fd.handle(ipc.MethodApprovalsDecide, ipc.GateUnlock, func(_ *ipc.ConnState, p json.RawMessage) (any, error) {
		decided = append(decided, string(p))
		return nil, nil
	})
	fd.start()
	p := &fakePrompter{passwords: []string{"pw"}, lines: []string{"v", "x", "a", "d", "s"}}
	r := fd.run(p, "approvals")
	if r.code != 0 {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	want := "" +
		"\nTask 1 of 3: T1 from gpu-box, received 2026-09-26 10:00 UTC\n" +
		"Size: 20 bytes, SHA-256: ab12\n" +
		"--- preview (first 500 characters) ---\n| run [2Jtests\n---\n" +
		"--- full text ---\n| run [2Jtests fully\n---\n" +
		"Approved T1.\n" +
		"\nTask 2 of 3: T2 from mac, received 2026-09-26 10:00 UTC\n" +
		"Size: 6 bytes, SHA-256: cd34\n" +
		"--- preview (first 500 characters) ---\n| rm -rf\n---\n" +
		"Denied T2.\n" +
		"\nTask 3 of 3: T3 from mac, received 2026-09-26 10:00 UTC\n" +
		"Size: 5 bytes, SHA-256: ef56\n" +
		"--- preview (first 500 characters) ---\n| later\n---\n" +
		"Skipped.\n"
	if r.stdout != want {
		t.Fatalf("stdout\n%q\nwant\n%q", r.stdout, want)
	}
	if strings.Contains(r.stdout, "\x1b") {
		t.Fatal("escape sequence reached the terminal")
	}
	if !slices.Equal(decided, []string{`{"task_id":"T1","approve":true}`, `{"task_id":"T2","approve":false}`}) {
		t.Fatalf("decided %v", decided)
	}
}

func TestApproveSingle(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodApprovalsDecide, ipc.GateUnlock, nil)
	fd.start()
	if r := fd.run(&fakePrompter{passwords: []string{"pw"}}, "deny", "T5"); r.stdout != "Denied T5.\n" {
		t.Fatalf("%q %q", r.stdout, r.stderr)
	}
	if fd.params(ipc.MethodApprovalsDecide) != `{"task_id":"T5","approve":false}` {
		t.Fatal(fd.params(ipc.MethodApprovalsDecide))
	}
}

func TestResetIdentityRequiresConfirmation(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{MachineID: "abcdefghijklmnopqrstuvwxyz"})
	fd.reply(ipc.MethodResetIdentity, ipc.GateUnlock, nil)
	fd.start()
	r := fd.run(&fakePrompter{lines: []string{"abcdefghijklmnoX"}}, "reset-identity")
	if r.code != 1 || !strings.Contains(r.stderr, "confirmation did not match") || slices.Contains(fd.methods(), ipc.MethodResetIdentity) {
		t.Fatalf("mismatch: %d %q %v", r.code, r.stderr, fd.methods())
	}
	r = fd.run(&fakePrompter{lines: []string{"abcdefghijklmnop"}, passwords: []string{"pw"}}, "reset-identity")
	if r.code != 0 || !strings.Contains(r.stdout, "Identity reset.") {
		t.Fatalf("confirmed: %d %q %q", r.code, r.stdout, r.stderr)
	}
}

func TestAllowPathMakesAbsolute(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodAllowPathAdd, ipc.GateUnlock, nil)
	fd.start()
	r := fd.run(&fakePrompter{passwords: []string{"pw"}}, "allow-path", "/data/shared/../models")
	if r.code != 0 || fd.params(ipc.MethodAllowPathAdd) != `{"path":"/data/models"}` {
		t.Fatalf("%d %s %s", r.code, fd.params(ipc.MethodAllowPathAdd), r.stderr)
	}
}

func TestStatusHumanAndJSON(t *testing.T) {
	fd := newFakeDaemon(t)
	st := ipc.StatusResult{MachineID: "abcdefghijklmnopqrstuvwxyz", DeviceName: "mac", RelayURL: "https://relay.example.com",
		RelayConnected: true, Peers: []ipc.PeerView{{Alias: "gpu-box", Online: true}},
		Sessions: []string{"claude@glow-v2"}, OutboxPending: 1, InboxUnread: 2, PendingApprovals: 3, Errors: []string{"clock skew"}}
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, st)
	fd.start()
	want := "" +
		"Machine:     mac (abcdefghijklmnop)\n" +
		"Relay:       https://relay.example.com (connected)\n" +
		"Kill switch: off\n" +
		"Peers:       gpu-box (online)\n" +
		"Sessions:    claude@glow-v2\n" +
		"Outbox:      1 pending, 0 held\n" +
		"Inbox:       2 unread\n" +
		"Approvals:   3 pending\n" +
		"Warning:     clock skew\n"
	if r := fd.run(nil, "status"); r.stdout != want {
		t.Fatalf("\n%s\nwant\n%s", r.stdout, want)
	}
	r := fd.run(nil, "status", "--json")
	var back ipc.StatusResult
	if err := json.Unmarshal([]byte(r.stdout), &back); err != nil || back.MachineID != st.MachineID || back.PendingApprovals != 3 {
		t.Fatalf("json: %v %s", err, r.stdout)
	}
}

func TestLog(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodAuditRead, ipc.GateAllowWhenKilled, ipc.AuditReadResult{Events: []audit.Event{
		{TS: paired, Type: "pair", Alias: "gpu-box", ItemID: "P1"},
		{TS: paired, Type: "kill"},
	}})
	fd.start()
	r := fd.run(nil, "log", "-n", "5")
	want := "2026-09-26T10:00:00Z  pair             gpu-box      P1\n2026-09-26T10:00:00Z  kill             -            -\n"
	if r.stdout != want || fd.params(ipc.MethodAuditRead) != `{"limit":5}` {
		t.Fatalf("%q %s", r.stdout, fd.params(ipc.MethodAuditRead))
	}
}

func TestFilesList(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodFilesList, ipc.GateNone, ipc.FilesListResult{Files: []ipc.FileView{
		{FileID: "F1", Direction: "in", Peer: "gpu-box", Name: "report.pdf", State: "declined", Size: 2048},
	}})
	fd.start()
	want := "FILE ID  DIR  PEER     STATE     SIZE  NAME\nF1       in   gpu-box  declined  2048  report.pdf\n"
	if r := fd.run(nil, "files"); r.stdout != want {
		t.Fatalf("%q", r.stdout)
	}
}

type mapSettings map[string]string

func (m mapSettings) GetSetting(_ context.Context, k string) (string, bool, error) {
	v, ok := m[k]
	return v, ok, nil
}
func (m mapSettings) SetSetting(_ context.Context, k, v string) error { m[k] = v; return nil }

func TestInitWritesConfigAndToken(t *testing.T) {
	fd := newFakeDaemon(t)
	settings := mapSettings{}
	env, out, _ := fd.env(&fakePrompter{}, "s3cret-token\n")
	env.OpenSettings = func(string) (store.SettingsStore, func() error, error) {
		return settings, func() error { return nil }, nil
	}
	if code := Main([]string{"init", "--relay", "https://relay.example.com", "--relay-token", "-"}, env); code != 0 {
		t.Fatalf("code %d", code)
	}
	paths, _ := env.Paths()
	cfg, err := config.Load(paths)
	if err != nil || cfg.RelayURL != "https://relay.example.com" || cfg.DeviceName != "prith-s-macbook" {
		t.Fatalf("%v %+v", err, cfg)
	}
	if settings[SettingRelayAdminToken] != "s3cret-token" {
		t.Fatalf("token %q", settings[SettingRelayAdminToken])
	}
	if !strings.Contains(out.String(), "Next: run `cravv-connect daemon install`") {
		t.Fatal(out.String())
	}
	env2, _, errb := fd.env(&fakePrompter{}, "")
	if code := Main([]string{"init", "--relay", "https://relay.example.com"}, env2); code != 1 || !strings.Contains(errb.String(), "already initialized") {
		t.Fatalf("second init: %d %q", code, errb.String())
	}
	env3, _, errb3 := fd.env(&fakePrompter{}, "")
	if code := Main([]string{"init", "--relay", "ftp://x", "--force"}, env3); code != 1 || !strings.Contains(errb3.String(), "http or https") {
		t.Fatalf("bad url: %q", errb3.String())
	}
	for _, bad := range []string{"https://relay.example.com/v1", "https://relay.example.com?x=1", "https://user@relay.example.com"} {
		env4, _, errb4 := fd.env(&fakePrompter{}, "")
		if code := Main([]string{"init", "--relay", bad, "--force"}, env4); code != 1 || !strings.Contains(errb4.String(), "scheme://host[:port]") {
			t.Fatalf("%s accepted: %q", bad, errb4.String())
		}
	}
	env5, _, _ := fd.env(&fakePrompter{}, "")
	if code := Main([]string{"init", "--relay", "HTTPS://Relay.Example.com:443/", "--force"}, env5); code != 0 {
		t.Fatal("normalizable origin refused")
	}
	if cfg, _ := config.Load(paths); cfg.RelayURL != "https://relay.example.com" {
		t.Fatalf("relay stored as %q, want the normalized origin", cfg.RelayURL)
	}
}

func TestSuggestAlias(t *testing.T) {
	for in, want := range map[string]string{
		"GPU Box!!": "gpu-box", "": "peer", "---": "peer", "a very long machine name indeed yes": "a-very-long-machine-name",
		"</remote_message>": "remote-message", "Prith's MacBook": "prith-s-macbook",
	} {
		if got := suggestAlias(in); got != want {
			t.Errorf("suggestAlias(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDaemonStatusAndStopWithoutDaemon(t *testing.T) {
	fd := newFakeDaemon(t)
	if r := fd.run(nil, "daemon", "status"); r.stdout != "Daemon is not running.\n" {
		t.Fatalf("%q", r.stdout)
	}
	if r := fd.run(nil, "daemon", "stop"); r.stdout != "Daemon is not running.\n" {
		t.Fatalf("%q", r.stdout)
	}
	os.WriteFile(filepath.Join(fd.home, "daemon.pid"), []byte("999999"), 0o600)
	if r := fd.run(nil, "daemon", "stop"); r.stdout != "Daemon is not running.\n" {
		t.Fatalf("stale pid: %q %q", r.stdout, r.stderr)
	}
}

type fakeService struct{ started, stopped bool }

func (s *fakeService) Installed() bool             { return true }
func (s *fakeService) Start(context.Context) error { s.started = true; return nil }
func (s *fakeService) Stop(context.Context) error  { s.stopped = true; return nil }

func TestDaemonStartUsesServiceOrSpawn(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{Version: BuildVersion()})
	// Not running yet: the service start is followed by a status probe. Start
	// serving only when Start is called.
	svc := &fakeService{}
	env, out, _ := fd.env(&fakePrompter{}, "")
	env.Service = serviceFunc{svc, func() { fd.start() }}
	if code := Main([]string{"daemon", "start"}, env); code != 0 || out.String() != "Daemon started.\n" || !svc.started {
		t.Fatalf("service: %q %v", out.String(), svc.started)
	}
	env2, out2, _ := fd.env(&fakePrompter{}, "")
	if code := Main([]string{"daemon", "start"}, env2); code != 0 || out2.String() != "Daemon is already running.\n" {
		t.Fatalf("already: %q", out2.String())
	}

	fd2 := newFakeDaemon(t)
	fd2.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{})
	env3, out3, _ := fd2.env(&fakePrompter{}, "")
	var spawned []string
	env3.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }
	env3.Spawn = func(exe string, args []string, logPath string) (int, error) {
		spawned = append([]string{exe}, args...)
		spawned = append(spawned, filepath.Base(logPath))
		fd2.start()
		return 4242, nil
	}
	if code := Main([]string{"daemon", "start"}, env3); code != 0 || out3.String() != "Daemon started.\n" {
		t.Fatalf("spawn: %q", out3.String())
	}
	// The daemon rotates its own log; the spawn only captures crash output.
	if !slices.Equal(spawned, []string{"/usr/local/bin/cravv-connect", "daemon", "run", "--log-file", filepath.Join(fd2.home, "daemon.log"), "daemon-stderr.log"}) {
		t.Fatalf("spawned %v", spawned)
	}
}

// serviceFunc runs onStart when Start is called.
type serviceFunc struct {
	*fakeService
	onStart func()
}

func (s serviceFunc) Start(ctx context.Context) error {
	s.onStart()
	return s.fakeService.Start(ctx)
}

func TestAuthUnavailableMessage(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodPairStart, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return nil, auth.ErrUnavailable
	})
	fd.start()
	r := fd.run(nil, "pair")
	if r.code != 1 || !strings.Contains(r.stderr, "built without PAM support") {
		t.Fatalf("%q", r.stderr)
	}
}

// auth_unavailable is shared by several auth errors; only the no-PAM build
// gets the rebuild advice, the others keep the daemon's own text.
func TestAuthUnavailableOtherCausesKeepMessage(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodPairStart, ipc.GateNone, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return nil, auth.ErrServiceNotAllowed
	})
	fd.start()
	r := fd.run(nil, "pair")
	if r.code != 1 || strings.Contains(r.stderr, "built without PAM support") || !strings.Contains(r.stderr, auth.ErrServiceNotAllowed.Error()) {
		t.Fatalf("%q", r.stderr)
	}
}

func TestInitDefaultNameStopsAtFirstDot(t *testing.T) {
	fd := newFakeDaemon(t)
	env, out, errb := fd.env(&fakePrompter{}, "")
	env.Hostname = func() (string, error) { return "GPU-Box.lan.example.com", nil }
	if code := Main([]string{"init", "--relay", "https://relay.example.com"}, env); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	paths, _ := env.Paths()
	if cfg, _ := config.Load(paths); cfg.DeviceName != "gpu-box" {
		t.Fatalf("device name %q, want gpu-box (%s)", cfg.DeviceName, out.String())
	}
}

// Moving to another relay with init --force: the old mailbox registration
// means nothing there, so the daemon must register again; peers are not told.
func TestInitForceNewRelayClearsRegistration(t *testing.T) {
	fd := newFakeDaemon(t)
	settings := mapSettings{}
	open := func(env *Env) {
		env.OpenSettings = func(string) (store.SettingsStore, func() error, error) {
			return settings, func() error { return nil }, nil
		}
	}
	env, _, _ := fd.env(&fakePrompter{}, "")
	open(env)
	if code := Main([]string{"init", "--relay", "https://old.example.com"}, env); code != 0 {
		t.Fatal("first init failed")
	}
	settings[settingRelayRegistered] = "1"
	settings[settingRelayInvite] = "INV"

	env2, out2, _ := fd.env(&fakePrompter{}, "")
	open(env2)
	if code := Main([]string{"init", "--relay", "https://OLD.example.com:443", "--force"}, env2); code != 0 {
		t.Fatal("same-relay init failed")
	}
	if settings[settingRelayRegistered] != "1" || strings.Contains(out2.String(), "pair") {
		t.Fatalf("same relay cleared the registration: %v %q", settings, out2.String())
	}

	env3, out3, _ := fd.env(&fakePrompter{}, "")
	open(env3)
	if code := Main([]string{"init", "--relay", "https://new.example.com", "--force"}, env3); code != 0 {
		t.Fatal("new-relay init failed")
	}
	if settings[settingRelayRegistered] != "" || settings[settingRelayInvite] != "" {
		t.Fatalf("registration kept after a relay change: %v", settings)
	}
	if !strings.Contains(out3.String(), "re-pair") || !strings.Contains(out3.String(), "https://old.example.com") {
		t.Fatalf("no note about peers: %q", out3.String())
	}
}

func TestInitSettingKeysMatchDaemon(t *testing.T) {
	if settingRelayRegistered != daemon.SettingRelayRegistered || settingRelayInvite != daemon.SettingRelayInvite ||
		SettingRelayAdminToken != daemon.SettingRelayAdminToken {
		t.Fatal("cli and daemon settings keys differ")
	}
}
