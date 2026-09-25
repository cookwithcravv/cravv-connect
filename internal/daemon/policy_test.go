package daemon

import (
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestTrustPolicyDecide(t *testing.T) {
	levels := []core.TrustLevel{core.TrustChatOnly, core.TrustAskFirst, core.TrustAutonomous}
	cases := []struct {
		kind core.Kind
		want [3]Decision // chat-only, ask-first, autonomous
	}{
		{core.KindChat, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindTaskCreate, [3]Decision{DecisionReject, DecisionHold, DecisionDeliver}},
		{core.KindFileOffer, [3]Decision{DecisionHold, DecisionDeliver, DecisionDeliver}},
		{core.KindTaskUpdate, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindTaskCancel, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindControlPrekey, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindControlStalePrekey, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindControlDelivered, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindControlPaused, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindControlResumed, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindControlUnpaired, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
		{core.KindControlRelayMoved, [3]Decision{DecisionDeliver, DecisionDeliver, DecisionDeliver}},
	}
	var p TrustPolicy
	for _, tc := range cases {
		for i, lvl := range levels {
			if got := p.Decide(lvl, tc.kind); got != tc.want[i] {
				t.Errorf("Decide(%s, %s) = %s, want %s", lvl, tc.kind, got, tc.want[i])
			}
		}
	}
}

func TestTrustPolicyInvalidLevelNeverWidens(t *testing.T) {
	var p TrustPolicy
	for _, lvl := range []core.TrustLevel{0, 4, -1} {
		if got := p.Decide(lvl, core.KindTaskCreate); got != DecisionReject {
			t.Errorf("task.create at level %d = %s, want reject", lvl, got)
		}
		if got := p.Decide(lvl, core.KindFileOffer); got != DecisionReject {
			t.Errorf("file.offer at level %d = %s, want reject", lvl, got)
		}
	}
}
