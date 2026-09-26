package cli

import (
	"bytes"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/qrcode"
)

const testJoinCode = "cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A" // https://relay.example.com

func pairDaemon(t *testing.T, relay string) *fakeDaemon {
	fd := pairDaemonUnstarted(t, relay)
	fd.start()
	return fd
}

// pairDaemonUnstarted answers status (with relay), pairing and joining.
func pairDaemonUnstarted(t *testing.T, relay string) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{Version: BuildVersion(), RelayURL: relay, RelayConnected: true})
	fd.reply(ipc.MethodPairStart, ipc.GateUnlock, ipc.PairStartResult{PendingID: "P1", Code: "CRAVV-7K3F-9QXM-TR2A"})
	fd.reply(ipc.MethodPairAwait, ipc.GateUnlock, ipc.PendingPeerResult{PendingID: "P1", SuggestedName: "gpu-box", MachineID: "m1"})
	fd.reply(ipc.MethodJoinStart, ipc.GateUnlock, ipc.PendingPeerResult{PendingID: "P2", SuggestedName: "mac", MachineID: "m2"})
	fd.reply(ipc.MethodPairFinalize, ipc.GateUnlock, ipc.PairFinalizeResult{Alias: "gpu-box"})
	return fd
}

// pair shows the join code with its QR code (upper case, for the compact
// alphanumeric mode) and the plain bind code for machines already set up.
func TestPairShowsJoinCodeAndQR(t *testing.T) {
	defer func(f func(io.Writer) bool) { isTerminal = f }(isTerminal)
	isTerminal = func(io.Writer) bool { return true }
	fd := pairDaemon(t, "https://relay.example.com")
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{""}}, "pair")
	if r.code != 0 {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	var qr bytes.Buffer
	if err := qrcode.Terminal(&qr, strings.ToUpper(testJoinCode)); err != nil {
		t.Fatal(err)
	}
	want := "" +
		"Join code: " + testJoinCode + "\n\n" +
		qr.String() + "\n" +
		"On a new machine run:\n" +
		"  cravv-connect setup --join " + testJoinCode + "\n" +
		"On a machine already set up for this relay run:\n" +
		"  cravv-connect join CRAVV-7K3F-9QXM-TR2A\n" +
		"The code works once and expires in 10 minutes.\n" +
		"Waiting for the other machine...\n"
	if !strings.HasPrefix(r.stdout, want) {
		t.Fatalf("stdout\n%s\nwant prefix\n%s", r.stdout, want)
	}
}

func TestPairNoQR(t *testing.T) {
	fd := pairDaemon(t, "https://relay.example.com")
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{""}}, "pair", "--no-qr")
	if r.code != 0 || strings.Contains(r.stdout, "\x1b") || !strings.Contains(r.stdout, "Join code: "+testJoinCode+"\n\nOn a new machine") {
		t.Fatalf("code %d stdout %q", r.code, r.stdout)
	}
}

// The QR code is drawn only on a terminal: piped or redirected output gets
// the join code as text.
func TestPairNoQROffATerminal(t *testing.T) {
	fd := pairDaemon(t, "https://relay.example.com")
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{""}}, "pair")
	if r.code != 0 || strings.Contains(r.stdout, "\x1b") || !strings.Contains(r.stdout, "Join code: "+testJoinCode+"\n\nOn a new machine") {
		t.Fatalf("code %d stdout %q", r.code, r.stdout)
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) || isTerminal(&bytes.Buffer{}) {
		t.Fatal("a file or a buffer counts as a terminal")
	}
}

// join takes a join code made on this machine's relay and joins with the
// bind code inside it.
func TestJoinAcceptsJoinCode(t *testing.T) {
	fd := pairDaemon(t, "https://relay.example.com")
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{""}}, "join", strings.ToUpper(testJoinCode))
	if r.code != 0 {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	if got := fd.params(ipc.MethodJoinStart); got != `{"code":"CRAVV-7K3F-9QXM-TR2A"}` {
		t.Fatalf("join params %s", got)
	}
}

// A join code made on another relay is refused before any password is asked
// and before the daemon is asked to join.
func TestJoinRefusesOtherRelay(t *testing.T) {
	fd := pairDaemon(t, "https://other.example.com")
	p := &fakePrompter{}
	r := fd.run(p, "join", testJoinCode)
	want := "error: the join code is for another relay: it is for https://relay.example.com, but this machine uses https://other.example.com. " +
		"To move this machine to that relay, run `cravv-connect setup --reset --join <code>` (you pair again with every peer).\n"
	if r.code != 1 || r.stderr != want {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	if slices.Contains(fd.methods(), ipc.MethodJoinStart) || len(p.asked) != 0 {
		t.Fatalf("joined anyway: %v, asked %v", fd.methods(), p.asked)
	}
}
