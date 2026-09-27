package daemon

import (
	"context"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/audit"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

func TestKillSwitchPersistsAndRunsHooks(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	lg := &d2Audit{}
	k, err := NewKillSwitch(ctx, st, lg)
	if err != nil {
		t.Fatal(err)
	}
	if k.Killed() {
		t.Fatal("fresh switch is on")
	}
	var calls []string
	k.SetHooks(KillHooks{
		BeforeKill: func(context.Context) {
			// While the flush runs the switch already reads as on (the IPC
			// gate and inbound stop), but the send loop may still send.
			if !k.Killed() {
				t.Error("Killed() false during the kill flush")
			}
			if !k.SendingAllowed() {
				t.Error("sending blocked during the kill flush")
			}
			calls = append(calls, "before")
		},
		AfterKill:   func(context.Context) { calls = append(calls, "after") },
		AfterResume: func(context.Context) { calls = append(calls, "resume") },
	})
	if err := k.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if err := k.Kill(ctx); err != nil {
		t.Fatal(err)
	}
	if k.SendingAllowed() {
		t.Fatal("sending still allowed after the kill")
	}
	if !k.Killed() || len(calls) != 2 || calls[0] != "before" || calls[1] != "after" {
		t.Fatalf("killed=%v calls=%v", k.Killed(), calls)
	}
	if v, _, _ := st.GetSetting(ctx, store.SettingKilled); v != "1" {
		t.Fatalf("persisted %q", v)
	}
	again, err := NewKillSwitch(ctx, st, lg)
	if err != nil || !again.Killed() {
		t.Fatalf("state not restored: %v", err)
	}
	if err := k.Resume(ctx, true); err != nil {
		t.Fatal(err)
	}
	if k.Killed() || !k.SendingAllowed() || calls[len(calls)-1] != "resume" {
		t.Fatalf("after resume killed=%v calls=%v", k.Killed(), calls)
	}
	if fresh, _ := NewKillSwitch(ctx, st, lg); fresh.Killed() {
		t.Fatal("resume not persisted")
	}
	if len(lg.ofType(audit.EvKill)) != 1 || len(lg.ofType(audit.EvKillResume)) != 1 {
		t.Fatalf("audit = %+v", lg.events)
	}
}
