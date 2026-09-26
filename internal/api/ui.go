package api

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// UIPort starts the local web UI and returns a URL with a one-time launch
// token.
type UIPort interface {
	Start(ctx context.Context) (string, error)
}

// LocalSessionPort lists this machine's shared sessions for the human and
// finds an open one by name. Views never carry session IDs.
type LocalSessionPort interface {
	Local(ctx context.Context) ([]ipc.SharedSessionView, error)
	// OpenByName returns the ID of the open session called name, or
	// core.ErrNotFound.
	OpenByName(ctx context.Context, name string) (string, error)
	// CloseByName closes the open or away session called name (a managed
	// one through its host, which stops its run) and returns its view.
	CloseByName(ctx context.Context, name string) (ipc.SharedSessionView, error)
}

// UIPorts are what the web UI methods need. They are kept apart from Ports
// so the UI group registers on its own (RegisterUI) and adds no field to
// the core port set.
type UIPorts struct {
	UI    UIPort
	Local LocalSessionPort
	Links LinkPort
}

// errHumanOnly refuses an agent connection (one that registered a session)
// a method meant for the owner's CLI or web UI.
var errHumanOnly = fmt.Errorf("%w: only the owner's CLI or web UI may call this, not an agent connection", ipc.ErrBadRequest)

type uiHandlers struct{ p UIPorts }

// RegisterUI adds the web UI methods to s.
func RegisterUI(s *ipc.Server, p UIPorts) {
	u := uiHandlers{p: p}
	// The UI can do nothing a local process could not do over IPC, and it
	// must work while killed so the human can resume from it.
	s.Register(ipc.MethodUIStart, ipc.Typed(u.start), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodSessionsLocal, ipc.Typed(u.local), ipc.GateAllowWhenKilled)
	// Asking for a link on a session's behalf lets the other side message
	// that chat once it accepts, like accepting a link at messages: from
	// the CLI or UI that decision needs the password.
	s.Register(ipc.MethodLinkConnectAs, ipc.Typed(u.connectAs), ipc.GateUnlock)
	// Closing a session is a cut-off, like disconnecting a link.
	s.Register(ipc.MethodSessionsClose, ipc.Typed(u.close), ipc.GateAllowWhenKilled)
}

func (u uiHandlers) close(ctx context.Context, cs *ipc.ConnState, p ipc.SessionNameParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	if err := required("name", p.Name); err != nil {
		return nil, err
	}
	return u.p.Local.CloseByName(ctx, p.Name)
}

func humanOnly(cs *ipc.ConnState) error {
	if cs.Session() != "" || cs.Shared() != "" {
		return errHumanOnly
	}
	return nil
}

func (u uiHandlers) start(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	if u.p.UI == nil {
		return nil, fmt.Errorf("%w: this daemon has no web UI", ipc.ErrBadRequest)
	}
	url, err := u.p.UI.Start(ctx)
	if err != nil {
		return nil, err
	}
	return ipc.UIStartResult{URL: url}, nil
}

func (u uiHandlers) local(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	list, err := u.p.Local.Local(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []ipc.SharedSessionView{}
	}
	return ipc.LocalSessionsResult{Sessions: list}, nil
}

func (u uiHandlers) connectAs(ctx context.Context, cs *ipc.ConnState, p ipc.LinkConnectAsParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	for _, f := range []struct{ name, value string }{{"session", p.Session}, {"target", p.Target}, {"permission", p.Permission}} {
		if err := required(f.name, f.value); err != nil {
			return nil, err
		}
	}
	id, err := u.p.Local.OpenByName(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	return u.p.Links.Connect(ctx, id, p.Target, p.Permission, p.Note)
}
