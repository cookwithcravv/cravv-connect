package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Human-only actions (spec 7.2) refuse unless the caller says the password
// unlock happened, so no API adapter can forget the gate.
func TestHumanOnlyActionsNeedUnlock(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	ask, _ := d2Peer(t, e.te.st, "mac", core.TrustAskFirst)
	held := e.te.incoming(t, ask, "", "held work")
	if err := e.te.tasks.Decide(ctx, held, true, false); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("Decide without unlock err = %v", err)
	}
	if tk := e.te.state(t, held); tk.State != core.TaskAwaitingApproval {
		t.Fatalf("task decided without unlock: %s", tk.State)
	}

	chatOnly, _ := d2Peer(t, e.te.st, "stranger", core.TrustChatOnly)
	body := e.blobs.put(t, "a.txt", []byte("a"))
	e.offer(t, chatOnly, body)
	if err := e.files.Accept(ctx, body.FileID, false); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("Accept without unlock err = %v", err)
	}
	if r := e.record(t, body.FileID); r.State != store.FileHeld {
		t.Fatalf("file accepted without unlock: %s", r.State)
	}

	if err := NewAllowPaths(e.te.st, nil).Add(ctx, t.TempDir(), false); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("AllowPaths.Add without unlock err = %v", err)
	}
	if roots, _ := NewAllowPaths(e.te.st, nil).List(ctx); len(roots) != 0 {
		t.Fatalf("root added without unlock: %v", roots)
	}

	k, err := NewKillSwitch(ctx, e.te.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := k.Resume(ctx, false); !errors.Is(err, core.ErrAuthRequired) || !k.Killed() {
		t.Fatalf("Resume without unlock err = %v killed = %v", err, k.Killed())
	}

	d := d2NewDaemon(t, t.TempDir(), &d2Relay{})
	defer d.Close()
	old := d.Identity().MachineID()
	if err := d.ResetIdentity(ctx, false); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("ResetIdentity without unlock err = %v", err)
	}
	if d.Identity().MachineID() != old || d.Kill().Killed() {
		t.Fatal("identity reset without unlock")
	}
}
