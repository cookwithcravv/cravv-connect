package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestDecisionStrings(t *testing.T) {
	for d, want := range map[Decision]string{DecisionDeliver: "deliver", DecisionHold: "hold", DecisionReject: "reject", 0: "unknown"} {
		if d.String() != want {
			t.Errorf("Decision(%d) = %q, want %q", d, d.String(), want)
		}
	}
}

func TestCheckGateDecision(t *testing.T) {
	ctx := context.Background()
	if err := checkGateDecision(ctx, DecisionReject, core.KindTaskCreate, "T"); err != nil {
		t.Fatalf("no gate ran: %v", err)
	}
	gated := withDecision(ctx, DecisionHold)
	if err := checkGateDecision(gated, DecisionHold, core.KindTaskCreate, "T"); err != nil {
		t.Fatalf("matching decision: %v", err)
	}
	err := checkGateDecision(gated, DecisionDeliver, core.KindTaskCreate, "T")
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("a handler that disagrees with the gate must fail: %v", err)
	}
}
