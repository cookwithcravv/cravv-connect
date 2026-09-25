package daemon

import "github.com/cravv/cravv-connect/internal/core"

// Decision is what the receiving daemon does with an incoming item.
type Decision int

const (
	DecisionDeliver Decision = iota + 1
	DecisionHold
	DecisionReject
)

func (d Decision) String() string {
	switch d {
	case DecisionDeliver:
		return "deliver"
	case DecisionHold:
		return "hold"
	case DecisionReject:
		return "reject"
	}
	return "unknown"
}

// TrustPolicy maps (incoming trust level, kind) to a decision (spec 7.1).
type TrustPolicy struct{}

// kindRules holds the kinds whose decision depends on the trust level.
// Anything not listed (chat, task.update, task.cancel, control.*) is delivered.
var kindRules = map[core.Kind]map[core.TrustLevel]Decision{
	core.KindTaskCreate: {
		core.TrustChatOnly:   DecisionReject,
		core.TrustAskFirst:   DecisionHold,
		core.TrustAutonomous: DecisionDeliver,
	},
	core.KindFileOffer: {
		core.TrustChatOnly:   DecisionHold,
		core.TrustAskFirst:   DecisionDeliver,
		core.TrustAutonomous: DecisionDeliver,
	},
}

// Decide returns the decision. An invalid trust level is treated as the most restrictive
// outcome for gated kinds (reject), so a corrupt record never widens access.
func (TrustPolicy) Decide(level core.TrustLevel, kind core.Kind) Decision {
	rules, gated := kindRules[kind]
	if !gated {
		return DecisionDeliver
	}
	if d, ok := rules[level]; ok {
		return d
	}
	return DecisionReject
}
