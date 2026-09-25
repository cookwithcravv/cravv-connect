package daemon

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

type decisionKey struct{}

// withDecision records the PolicyGate's decision for the wrapped handler.
func withDecision(ctx context.Context, d Decision) context.Context {
	return context.WithValue(ctx, decisionKey{}, d)
}

// DecisionFrom returns the decision a PolicyGate made for this envelope, if any.
func DecisionFrom(ctx context.Context) (Decision, bool) {
	d, ok := ctx.Value(decisionKey{}).(Decision)
	return d, ok
}

// checkGateDecision is the handlers' defense in depth: when a PolicyGate ran, the
// decision the handler computed itself must match the gate's.
func checkGateDecision(ctx context.Context, own Decision, kind core.Kind, id string) error {
	if gate, ok := DecisionFrom(ctx); ok && gate != own {
		return fmt.Errorf("%s %s: policy decision mismatch (gate %s, handler %s)", kind, id, gate, own)
	}
	return nil
}

// PolicyGate enforces the trust policy (spec 7.1) in front of a handler, so a handler
// that forgets to check it still cannot act on a rejected item. A rejected envelope
// never reaches Inner: it goes to OnReject (which records the refusal and tells the
// sender), or is dropped when OnReject is nil. Hold and Deliver reach Inner with the
// decision in the context (DecisionFrom).
type PolicyGate struct {
	Inner    Handler
	OnReject Handler
	Policy   TrustPolicy
}

// Handle implements Handler.
func (g PolicyGate) Handle(ctx context.Context, peer store.Peer, env core.Envelope) error {
	d := g.Policy.Decide(peer.TrustIn, env.Kind)
	ctx = withDecision(ctx, d)
	if d == DecisionReject {
		if g.OnReject == nil {
			return nil
		}
		return g.OnReject.Handle(ctx, peer, env)
	}
	return g.Inner.Handle(ctx, peer, env)
}
