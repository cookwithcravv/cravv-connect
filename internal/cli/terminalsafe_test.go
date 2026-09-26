package cli

import (
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// hostile holds peer-chosen strings that try to rewrite or fake terminal output.
const (
	hostileID    = "T1\n\nTask 2 of 2: 01J8ZR0A1B2C3D4E5F6G7H8J9K from boss\x1b[8m"
	hostileName  = "gpu\u202ebox\u200b\t\r"
	hostileBlock = "fine\n---\nApproved T9.\n\x1b[2J\u2066hidden\u2069\u00a0\tend"
)

// requireTerminalClean fails when out holds anything a peer could use to move
// the cursor, hide text or reorder it.
func requireTerminalClean(t *testing.T, out string) {
	t.Helper()
	for _, r := range out {
		switch {
		case r == '\n':
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			t.Fatalf("control character %U in output:\n%q", r, out)
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r >= 0x200b && r <= 0x200f, r == 0xfeff:
			t.Fatalf("invisible or bidi character %U in output:\n%q", r, out)
		}
	}
}

func TestTerminalSafeStripsEverything(t *testing.T) {
	in := "a\x1b[31mred\x07\u009b\nb\tc\r\u202ed\u2066e\u200bf\ufeffg\U000e0041h\u2028i"
	if got := terminalSafe(in); got != "a[31mredbcdefghi" {
		t.Fatalf("terminalSafe = %q", got)
	}
}

func TestTerminalBlockPrefixesEveryLine(t *testing.T) {
	got := terminalBlock("one\n\x1b[2Jtwo\r\n\tthree\u202e")
	want := "| one\n| [2Jtwo\n|     three"
	if got != want {
		t.Fatalf("terminalBlock = %q, want %q", got, want)
	}
}

func TestApprovalsHostileStrings(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodApprovalsList, ipc.GateUnlock, ipc.ApprovalsListResult{Tasks: []ipc.ApprovalView{
		{TaskID: hostileID, Peer: hostileName, Preview: hostileBlock, Full: hostileBlock, SHA256: "ab\n12", Size: 1, Received: paired},
	}})
	fd.reply(ipc.MethodApprovalsDecide, ipc.GateUnlock, nil)
	fd.start()
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{"v", "a"}}, "approvals")
	if r.code != 0 {
		t.Fatalf("%d %s", r.code, r.stderr)
	}
	requireTerminalClean(t, r.stdout)
	lines := strings.Split(r.stdout, "\n")
	tasks, approved := 0, 0
	for _, l := range lines {
		if strings.HasPrefix(l, "Task ") {
			tasks++
		}
		if strings.HasPrefix(l, "Approved ") {
			approved++
		}
	}
	if tasks != 1 || approved != 1 {
		t.Fatalf("hostile text faked screen lines (tasks %d, approved %d):\n%s", tasks, approved, r.stdout)
	}
	if !strings.Contains(r.stdout, "| Approved T9.") {
		t.Fatalf("preview lines are not marked as peer text:\n%s", r.stdout)
	}
}

func TestListingsHostileStrings(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{
		MachineID: "abc", DeviceName: hostileName, RelayURL: "https://r\x1b[8m",
		Peers:    []ipc.PeerView{{Alias: hostileName}},
		Sessions: []string{hostileName}, Errors: []string{hostileBlock},
	})
	fd.reply(ipc.MethodAuditRead, ipc.GateAllowWhenKilled, ipc.AuditReadResult{Events: []audit.Event{
		{TS: paired, Type: "task\n", Alias: hostileName, ItemID: hostileID},
	}})
	fd.reply(ipc.MethodFilesList, ipc.GateNone, ipc.FilesListResult{Files: []ipc.FileView{
		{FileID: hostileID, Direction: "in", Peer: hostileName, Name: hostileBlock, State: "held\n", Size: 1},
	}})
	fd.reply(ipc.MethodPeerList, ipc.GateNone, ipc.PeerListResult{Peers: []ipc.PeerView{
		{Alias: hostileName, MachineID: "m\x1b[8m"},
	}})
	fd.start()
	for _, args := range [][]string{{"status"}, {"log"}, {"files"}, {"peers"}} {
		r := fd.run(nil, args...)
		if r.code != 0 {
			t.Fatalf("%v: %d %s", args, r.code, r.stderr)
		}
		requireTerminalClean(t, r.stdout)
		want := map[string]int{"status": 9, "log": 1, "files": 2, "peers": 2}[args[0]]
		if n := strings.Count(r.stdout, "\n"); n != want {
			t.Fatalf("%v: %d lines, want %d:\n%s", args, n, want, r.stdout)
		}
	}
}
