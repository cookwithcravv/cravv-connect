package daemon

import (
	"context"
	"errors"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Authority is how a human decision reached the daemon (v2 spec section 10).
type Authority int

const (
	// AuthNone: no human decision was shown; any agent or local process.
	AuthNone Authority = iota
	// AuthChat: the human answered in the chat outside the model's control
	// (MCP elicitation or a confirmation code). A Decider produces it.
	AuthChat
	// AuthPassword: the login password, checked by the daemon's Guard.
	AuthPassword
)

func (a Authority) String() string {
	switch a {
	case AuthChat:
		return "chat"
	case AuthPassword:
		return "password"
	}
	return "none"
}

// grantAuthority is the tier needed to accept a link at perm: tasks-auto
// needs the password, the lower levels a chat decision or the password.
func grantAuthority(perm core.Permission) Authority {
	if perm == core.PermTasksAuto {
		return AuthPassword
	}
	return AuthChat
}

// DecisionRequest is one pending item shown to a human. Alias is the local
// alias of the peer machine; every other string may be peer-chosen.
type DecisionRequest struct {
	Link  store.Link
	Alias string
	Task  *store.Task // set for a single tasks-ask task; nil for a link request
}

// DecisionAnswer is the human's answer. Permission is the level granted when
// accepting a link (at most the one proposed; "" means the proposed level).
type DecisionAnswer struct {
	Accept     bool
	Permission core.Permission
}

// ErrNoDecision means the human gave no answer: the form was declined,
// dismissed or never shown. The item stays pending.
var ErrNoDecision = errors.New("no decision: the human did not answer, so the request stays pending")

// Decider asks the human behind a chat to decide, outside the model's
// control. Phase 2 implements it with MCP elicitation and confirmation codes;
// its answers carry AuthChat, so they can never grant tasks-auto.
type Decider interface {
	Decide(ctx context.Context, req DecisionRequest) (DecisionAnswer, error)
}

// NoDecider is the Phase 1 Decider: no chat channel exists yet, so every
// request stays pending for the CLI password path.
type NoDecider struct{}

// Decide always returns ErrNoDecision.
func (NoDecider) Decide(context.Context, DecisionRequest) (DecisionAnswer, error) {
	return DecisionAnswer{}, ErrNoDecision
}
