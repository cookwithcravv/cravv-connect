package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// Human-only actions (spec 7.2, v2 spec 10) refuse unless the caller says a
// human decided, so no API adapter can forget the gate.
func TestHumanOnlyActionsNeedUnlock(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	askLink := d2Link(t, e.te.st, e.te.peer, e.te.session, "asker", core.PermTasksAsk, core.PermMessages)
	held := core.NewID()
	if err := e.te.handle(t, d2Env(t, e.te.peer, core.KindTaskCreate, askLink.ID, core.TaskCreateBody{TaskID: held, Instructions: "held work"})); err != nil {
		t.Fatal(err)
	}
	if err := e.te.tasks.Decide(ctx, held, true, AuthNone); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("Decide without a human err = %v", err)
	}
	if tk := e.te.state(t, held); tk.State != core.TaskAwaitingApproval {
		t.Fatalf("task decided without unlock: %s", tk.State)
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
