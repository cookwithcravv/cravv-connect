package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// OfferPort edits the managed-session offer rules. Set and Remove take
// unlocked, the connection's password state; the daemon checks it too.
type OfferPort interface {
	List(ctx context.Context, machine string) ([]ipc.OfferView, error)
	Set(ctx context.Context, p ipc.OfferSetParams, unlocked bool) (ipc.OfferView, error)
	Remove(ctx context.Context, machine, label string, unlocked bool) error
}

// ManagedPort shows, opens and closes managed sessions. Open holds the
// session's queue until release is called.
type ManagedPort interface {
	List(ctx context.Context) ([]ipc.ManagedView, error)
	Open(ctx context.Context, name string) (res ipc.ManagedOpenResult, release func(), err error)
	Close(ctx context.Context, name string) error
}

// RunBinding is the managed session a run token bound a connection to.
type RunBinding struct {
	ID     string // kept in the connection state
	Folder string // the managed session's folder: files are sent from it
	View   ipc.SharedSessionView
}

// RunPort binds a connection to the managed session of the run holding a
// run token.
type RunPort interface {
	Bind(ctx context.Context, token string, conn uint64) (RunBinding, error)
}

// ManagedPorts are what the managed-session methods need; like UIPorts
// they register on their own (RegisterManaged).
type ManagedPorts struct {
	Offers  OfferPort
	Managed ManagedPort
	Runs    RunPort
}

type managedHandlers struct{ p ManagedPorts }

// RegisterManaged adds the managed-session methods to s.
func RegisterManaged(s *ipc.Server, p ManagedPorts) {
	m := managedHandlers{p: p}
	s.Register(ipc.MethodSessionRunBind, ipc.Typed(m.runBind), ipc.GateSession)
	// Offer rules: reading them is the owner's, editing them needs the
	// password (v2 spec 10).
	s.Register(ipc.MethodOffersList, ipc.Typed(m.offersList), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodOffersSet, ipc.Typed(m.offersSet), ipc.GateUnlock)
	s.Register(ipc.MethodOffersRemove, ipc.Typed(m.offersRemove), ipc.GateUnlock)
	s.Register(ipc.MethodManagedList, ipc.Typed(m.managedList), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodManagedOpen, ipc.Typed(m.managedOpen), ipc.GateNone)
	// Closing is a cut-off: no password, and it works while killed.
	s.Register(ipc.MethodManagedClose, ipc.Typed(m.managedClose), ipc.GateAllowWhenKilled)
}

// runBind binds this connection to a managed run's session. A connection
// that shares a chat's session cannot become a run.
func (m managedHandlers) runBind(ctx context.Context, cs *ipc.ConnState, p ipc.RunBindParams) (any, error) {
	if err := required("run_token", p.RunToken); err != nil {
		return nil, err
	}
	if cs.Shared() != "" && !cs.RunBound() {
		return nil, badRequest("this connection already shares a session")
	}
	b, err := m.p.Runs.Bind(ctx, p.RunToken, cs.ID())
	if err != nil {
		return nil, err
	}
	cs.SetShared(b.ID)
	cs.SetRunBound(b.Folder)
	return b.View, nil
}

func (m managedHandlers) offersList(ctx context.Context, cs *ipc.ConnState, p ipc.OffersListParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	list, err := m.p.Offers.List(ctx, p.Machine)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []ipc.OfferView{}
	}
	return ipc.OffersListResult{Offers: list}, nil
}

func (m managedHandlers) offersSet(ctx context.Context, cs *ipc.ConnState, p ipc.OfferSetParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	for _, f := range []struct{ name, value string }{{"machine", p.Machine}, {"label", p.Label}, {"folder", p.Folder}, {"permission", p.Permission}} {
		if err := required(f.name, f.value); err != nil {
			return nil, err
		}
	}
	if err := absPath("folder", p.Folder); err != nil {
		return nil, err
	}
	return m.p.Offers.Set(ctx, p, cs.Unlocked())
}

func (m managedHandlers) offersRemove(ctx context.Context, cs *ipc.ConnState, p ipc.OfferRemoveParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	if err := required("machine", p.Machine); err != nil {
		return nil, err
	}
	if err := required("label", p.Label); err != nil {
		return nil, err
	}
	return nil, m.p.Offers.Remove(ctx, p.Machine, p.Label, cs.Unlocked())
}

func (m managedHandlers) managedList(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	list, err := m.p.Managed.List(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []ipc.ManagedView{}
	}
	return ipc.ManagedListResult{Sessions: list}, nil
}

// managedOpen holds the session's queue for as long as this connection
// stays open: `cravv-connect session open` keeps it open while the human
// has the conversation.
func (m managedHandlers) managedOpen(ctx context.Context, cs *ipc.ConnState, p ipc.ManagedNameParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	if err := required("name", p.Name); err != nil {
		return nil, err
	}
	res, release, err := m.p.Managed.Open(ctx, p.Name)
	if err != nil {
		return nil, err
	}
	cs.OnClose(release)
	return res, nil
}

func (m managedHandlers) managedClose(ctx context.Context, cs *ipc.ConnState, p ipc.ManagedNameParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	if err := required("name", p.Name); err != nil {
		return nil, err
	}
	return nil, m.p.Managed.Close(ctx, p.Name)
}
