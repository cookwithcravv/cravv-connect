package daemon

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
)

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

type decisionKey struct{}

// withDecision records the gate's decision for the wrapped handler.
func withDecision(ctx context.Context, d Decision) context.Context {
	return context.WithValue(ctx, decisionKey{}, d)
}

// DecisionFrom returns the decision a LinkGate made for this envelope, if any.
func DecisionFrom(ctx context.Context) (Decision, bool) {
	d, ok := ctx.Value(decisionKey{}).(Decision)
	return d, ok
}

// checkGateDecision is the handlers' defense in depth: when a gate ran, the
// decision the handler computed itself must match the gate's.
func checkGateDecision(ctx context.Context, own Decision, kind core.Kind, id string) error {
	if gate, ok := DecisionFrom(ctx); ok && gate != own {
		return fmt.Errorf("%s %s: policy decision mismatch (gate %s, handler %s)", kind, id, gate, own)
	}
	return nil
}
