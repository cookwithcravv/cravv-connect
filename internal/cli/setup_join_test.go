package cli

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/joincode"
)

func joinRig(t *testing.T) *setupRig {
	return newSetupRigOn(t, pairDaemonUnstarted(t, "https://relay.example.com"))
}

func codeFor(t *testing.T, relay string) string {
	t.Helper()
	c, err := joincode.New(relay, bindcode.Code{Nameplate: "7K3F", Secret: "9QXMTR2A"})
	if err != nil {
		t.Fatal(err)
	}
	return c.String()
}

// A new machine: the relay is shown and confirmed, then init (no admin
// token), the login service, join with the password, the alias, and the
// agents, in that order.
func TestSetupJoinNewMachine(t *testing.T) {
	r := joinRig(t)
	r.prompt.lines = []string{"y", "", ""}
	r.prompt.passwords = []string{"pw"}
	if code := r.run("--join", testJoinCode); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	wantAsked := []string{
		"line: Join relay https://relay.example.com? (y/N)",
		"password: " + passwordPrompt,
		"line: Local name for this peer",
		"line: Add cravv-connect to claude? (Y/n)",
	}
	if !slices.Equal(r.prompt.asked, wantAsked) {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	if got := r.fd.params(ipc.MethodJoinStart); got != `{"code":"CRAVV-7K3F-9QXM-TR2A"}` {
		t.Fatalf("join params %s", got)
	}
	if cfg := r.config(t); cfg.RelayURL != "https://relay.example.com" {
		t.Fatalf("relay %q", cfg.RelayURL)
	}
	if _, ok := r.settings[SettingRelayAdminToken]; ok {
		t.Fatal("a joining machine stored an admin token")
	}
	if !slices.Equal(r.daemon.events, []string{"install /usr/local/bin/cravv-connect"}) || r.claude.installed == "" {
		t.Fatalf("service %v claude %q", r.daemon.events, r.claude.installed)
	}
	out := r.out.String()
	for _, want := range []string{"\n== Join ==\nConnected to machine m2.\nPaired with gpu-box.", "Connected to the relay https://relay.example.com.\n\n== Agents ==", "Setup is complete."} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

// Declining the relay changes nothing.
func TestSetupJoinDeclined(t *testing.T) {
	r := joinRig(t)
	r.prompt.lines = []string{""}
	if code := r.run("--join", testJoinCode); code != 0 || !strings.HasSuffix(r.out.String(), "Nothing changed.\n") {
		t.Fatalf("code %d stdout %q", code, r.out.String())
	}
	r.notConfigured(t)
	if len(r.daemon.events) != 0 || slices.Contains(r.fd.methods(), ipc.MethodJoinStart) {
		t.Fatal("joined anyway")
	}
}

// The relay in a join code comes from another machine: plain http is refused
// unless it is this machine or a private network, before anything is asked.
func TestSetupJoinRelayRule(t *testing.T) {
	for relay, ok := range map[string]bool{
		"http://relay.example.com":   false,
		"http://203.0.113.9:8787":    false,
		"http://192.168.1.10:8787":   true,
		"http://mac.local:8787":      true,
		"http://100.100.1.2:8787":    true,
		"https://relay.example.com":  true,
		"http://[fd00::1]:8787":      true,
		"http://relay.example.local": false,
	} {
		_, err := setupJoinCode(codeFor(t, relay))
		if (err == nil) != ok {
			t.Errorf("%s: %v", relay, err)
		}
	}
	r := joinRig(t)
	if code := r.run("--join", codeFor(t, "http://relay.example.com")); code != 1 ||
		r.errb.String() != "error: refusing the relay in this join code (http://relay.example.com): invalid relay URL: plain http is only allowed for localhost or a private network address\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if len(r.prompt.asked) != 0 {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	r.notConfigured(t)
}

// A machine set up for another relay needs --reset; one set up for the same
// relay just joins (no init, no relay question).
func TestSetupJoinOnASetUpMachine(t *testing.T) {
	r := joinRig(t)
	r.configure(t, "https://other.example.com")
	if code := r.run("--join", testJoinCode); code != 1 || r.errb.String() != "error: this machine is set up for relay https://other.example.com, "+
		"but the join code is for https://relay.example.com; to move it, run `cravv-connect setup --reset --join <code>` (you pair again with every peer)\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if len(r.prompt.asked) != 0 || r.config(t).RelayURL != "https://other.example.com" {
		t.Fatal("setup changed the machine")
	}

	r2 := joinRig(t)
	r2.configure(t, "https://relay.example.com")
	r2.daemon.up()
	r2.prompt.lines, r2.prompt.passwords = []string{"laptop"}, []string{"pw"}
	if code := r2.run("--join", testJoinCode, "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r2.errb.String())
	}
	if !slices.Equal(r2.prompt.asked, []string{"password: " + passwordPrompt, "line: Local name for this peer"}) || len(r2.daemon.events) != 0 {
		t.Fatalf("asked %q service %v", r2.prompt.asked, r2.daemon.events)
	}
	if !strings.Contains(r2.fd.params(ipc.MethodPairFinalize), `"alias":"laptop"`) || r2.config(t).DeviceName != "mac" {
		t.Fatalf("finalize %s", r2.fd.params(ipc.MethodPairFinalize))
	}
}

// --reset --join moves a machine to the join code's relay after both
// questions.
func TestSetupResetJoin(t *testing.T) {
	r := joinRig(t)
	r.configure(t, "https://other.example.com")
	r.daemon.up()
	r.prompt.lines, r.prompt.passwords = []string{"y", "y", ""}, []string{"pw"}
	if code := r.run("--reset", "--join", testJoinCode, "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if r.config(t).RelayURL != "https://relay.example.com" || !slices.Equal(r.daemon.events, []string{"stop", "install /usr/local/bin/cravv-connect"}) {
		t.Fatalf("relay %q service %v", r.config(t).RelayURL, r.daemon.events)
	}
}

func TestSetupJoinInputs(t *testing.T) {
	r := joinRig(t)
	if code := r.run("--join", "CRAVV-7K3F-9QXM-TR2A"); code != 1 ||
		r.errb.String() != "error: setup --join needs a join code (cravv-join:...): run `cravv-connect pair` on the other machine to show one\n" {
		t.Fatalf("bind code: %d %q", code, r.errb.String())
	}
	if code := r.run("--join", "cravv-join:!!:7K3F-9QXMTR2A"); code != 1 || !strings.Contains(r.errb.String(), "invalid join code") {
		t.Fatalf("garbage: %d %q", code, r.errb.String())
	}
	if code := r.run("--join", testJoinCode, "--relay", "https://relay.example.com"); code != 1 || !strings.Contains(r.errb.String(), "[join relay] were all set") {
		t.Fatalf("with --relay: %d %q", code, r.errb.String())
	}
	r.notConfigured(t)
}

// --reset --join keeps the daemon running until the new relay is confirmed
// and answers.
func TestSetupResetJoinKeepsDaemonUntilConfirmed(t *testing.T) {
	r := joinRig(t)
	r.configure(t, "https://other.example.com")
	r.daemon.up()
	r.prompt.lines = []string{"y", "n"}
	if code := r.run("--reset", "--join", testJoinCode, "--no-agents"); code != 0 || !strings.HasSuffix(r.out.String(), "Nothing changed.\n") {
		t.Fatalf("code %d stdout %q stderr %q", code, r.out.String(), r.errb.String())
	}
	if len(r.daemon.events) != 0 || !daemonUp(context.Background(), r.env) || r.config(t).RelayURL != "https://other.example.com" {
		t.Fatalf("service %v relay %q", r.daemon.events, r.config(t).RelayURL)
	}

	r2 := joinRig(t)
	r2.configure(t, "https://other.example.com")
	r2.daemon.up()
	r2.prompt.lines = []string{"y", "y"}
	if code := r2.run("--reset", "--join", codeFor(t, "https://down.example.com"), "--no-agents"); code != 1 ||
		r2.errb.String() != "error: the relay https://down.example.com does not answer: connection refused. "+
			"Nothing changed: this machine still uses relay https://other.example.com\n" {
		t.Fatalf("code %d stderr %q", code, r2.errb.String())
	}
	if len(r2.daemon.events) != 0 || !daemonUp(context.Background(), r2.env) {
		t.Fatalf("service %v", r2.daemon.events)
	}
}
