package app

import (
	"context"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/cravv/cravv-connect/internal/store"
)

// shared adapts the daemon's SessionService and AttentionService to api.SharedPort.
type shared struct{ d *daemon.Daemon }

func (a shared) view(ctx context.Context, s store.SharedSession) ipc.SharedSessionView {
	return ipc.SharedSessionView{
		Name: s.Name, Purpose: s.Purpose, Visibility: daemon.FormatVisibility(ctx, s.Visibility, a.d.Peers()),
		State: string(s.State), Kind: string(s.Kind), Agent: s.Agent,
	}
}

func (a shared) Share(ctx context.Context, conn uint64, agent, dir, name, purpose, visibility string) (string, ipc.ShareResult, error) {
	// No visibility leaves it unset: new sessions are private, and a
	// takeover of the chat's away session keeps what it had.
	var vis core.Visibility
	if strings.TrimSpace(visibility) != "" {
		var err error
		if vis, err = daemon.ParseVisibility(ctx, visibility, a.d.Peers()); err != nil {
			return "", ipc.ShareResult{}, err
		}
	}
	sh, err := a.d.Shared().Share(ctx, conn, daemon.ShareRequest{Agent: agent, ProjectDir: dir, Name: name, Purpose: purpose, Visibility: vis})
	if err != nil {
		return "", ipc.ShareResult{}, err
	}
	return sh.Session.ID, ipc.ShareResult{Session: a.view(ctx, sh.Session), WakeToken: sh.WakeToken, ReattachToken: sh.ReattachToken, Resumed: sh.Resumed}, nil
}

func (a shared) Current(ctx context.Context, id string, conn uint64) error {
	_, err := a.d.Shared().Current(ctx, id, conn)
	return err
}

func (a shared) Close(ctx context.Context, id string) error { return a.d.Shared().Close(ctx, id) }

func (a shared) Set(ctx context.Context, id string, purpose, visibility *string) (ipc.SharedSessionView, error) {
	var vis *core.Visibility
	if visibility != nil {
		v, err := daemon.ParseVisibility(ctx, *visibility, a.d.Peers())
		if err != nil {
			return ipc.SharedSessionView{}, err
		}
		vis = &v
	}
	s, err := a.d.Shared().Set(ctx, id, purpose, vis)
	if err != nil {
		return ipc.SharedSessionView{}, err
	}
	return a.view(ctx, s), nil
}

func (a shared) Reattach(ctx context.Context, conn uint64, token, agent, dir string) (string, ipc.SharedSessionView, error) {
	s, err := a.d.Shared().Reattach(ctx, conn, token, agent, dir)
	if err != nil {
		return "", ipc.SharedSessionView{}, err
	}
	return s.ID, a.view(ctx, s), nil
}

func (a shared) Detach(ctx context.Context, id string, conn uint64) error {
	return a.d.Shared().Detach(ctx, id, conn)
}

func (a shared) Listen(ctx context.Context, token string, timeout time.Duration) (ipc.ListenResult, error) {
	c, err := a.d.Attention().Listen(ctx, token, timeout)
	return listenResult(c), err
}

// listenResult maps daemon counts to the wire view.
func listenResult(c daemon.Counts) ipc.ListenResult {
	r := ipc.ListenResult{Unread: c.Unread, Requests: c.Requests, Approvals: c.Approvals, Closed: c.Closed}
	for _, g := range c.Groups {
		r.Pending = append(r.Pending, ipc.PendingCount{Link: g.Link, Machine: g.Machine, Kind: g.Kind, Count: g.Count})
	}
	return r
}

// discovery adapts the daemon's Discovery to api.DiscoveryPort.
type discovery struct{ d *daemon.Daemon }

func (a discovery) Sessions(ctx context.Context, machine string) (ipc.SessionsListResult, error) {
	peer, listed, err := a.d.Discovery().List(ctx, machine)
	if err != nil {
		return ipc.SessionsListResult{}, err
	}
	out := ipc.SessionsListResult{Machine: peer.Alias, Sessions: []ipc.RemoteSessionView{}}
	for _, s := range listed.Sessions {
		v := ipc.RemoteSessionView{Name: s.Name, Kind: string(s.Kind), Agent: s.Agent, State: string(s.State)}
		if s.Purpose != "" {
			v.Wrapped = present.Wrap(present.Item{Alias: peer.Alias, Session: s.Name, ID: s.SessionID, Kind: "session", Body: "purpose: " + s.Purpose})
		}
		out.Sessions = append(out.Sessions, v)
	}
	for _, o := range listed.Offers {
		out.Offers = append(out.Offers, ipc.RemoteOfferView{Label: o.Label, Agent: o.Agent, MaxPermission: string(o.MaxPermission)})
	}
	return out, nil
}

// links adapts the daemon's LinkService to api.LinkPort.
type links struct{ d *daemon.Daemon }

func authority(unlocked bool) daemon.Authority {
	if unlocked {
		return daemon.AuthPassword
	}
	return daemon.AuthNone
}

func permission(s string) (core.Permission, error) {
	p, err := core.ParsePermission(s)
	if err != nil {
		return "", daemon.ErrBadPermission
	}
	return p, nil
}

// view renders a link. Peer free text (purpose, note) is only in Wrapped.
func (a links) view(ctx context.Context, l store.Link) ipc.LinkView {
	alias := l.Peer.Short()
	if p, _, err := a.d.Peers().Resolve(ctx, string(l.Peer)); err == nil {
		alias = p.Alias
	}
	v := ipc.LinkView{
		Link: l.Num, Machine: alias, RemoteSession: l.RemoteName, Direction: string(l.Direction), State: string(l.State),
		PermissionIn: string(l.PermissionIn), PermissionOut: string(l.PermissionOut), Reason: l.Reason,
	}
	if l.State == store.LinkActive {
		v.Unreachable = !l.PresenceAway.IsZero()
		v.RemoteAway = l.RemoteAway || v.Unreachable
	}
	if s, err := a.d.Shared().Get(ctx, l.Session); err == nil {
		v.Session = s.Name
	}
	if l.State == store.LinkPending {
		v.Proposed = string(l.Proposed)
	}
	var body []string
	if l.RemotePurpose != "" {
		body = append(body, "purpose: "+l.RemotePurpose)
	}
	if l.Note != "" && l.State == store.LinkPending {
		body = append(body, "note: "+l.Note)
	}
	if len(body) > 0 {
		v.Wrapped = present.Wrap(present.Item{Alias: alias, Session: l.RemoteName, ID: l.ID, Kind: "link", Body: strings.Join(body, "\n")})
	}
	return v
}

// result renders the link a daemon call returned.
func (a links) result(ctx context.Context) func(store.Link, error) (ipc.LinkView, error) {
	return func(l store.Link, err error) (ipc.LinkView, error) {
		if err != nil {
			return ipc.LinkView{}, err
		}
		return a.view(ctx, l), nil
	}
}

func (a links) Connect(ctx context.Context, sessionID, target, perm, note string) (ipc.LinkView, error) {
	p, err := permission(perm)
	if err != nil {
		return ipc.LinkView{}, err
	}
	return a.result(ctx)(a.d.Links().Connect(ctx, sessionID, target, p, note))
}

func (a links) List(ctx context.Context, sessionID string) ([]ipc.LinkView, error) {
	ls, err := a.d.Links().List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.LinkView, 0, len(ls))
	for _, l := range ls {
		out = append(out, a.view(ctx, l))
	}
	return out, nil
}

func (a links) Disconnect(ctx context.Context, sessionID string, num int64) error {
	return a.d.Links().Disconnect(ctx, sessionID, num)
}

func (a links) Restrict(ctx context.Context, sessionID string, num int64, perm string) (ipc.LinkView, error) {
	p, err := permission(perm)
	if err != nil {
		return ipc.LinkView{}, err
	}
	return a.result(ctx)(a.d.Links().SetPermission(ctx, sessionID, num, p, daemon.AuthNone))
}

func (a links) Permit(ctx context.Context, num int64, perm string, unlocked bool) (ipc.LinkView, error) {
	p, err := permission(perm)
	if err != nil {
		return ipc.LinkView{}, err
	}
	return a.result(ctx)(a.d.Links().SetPermission(ctx, "", num, p, authority(unlocked)))
}

func (a links) Decide(ctx context.Context, sessionID string, num int64, accept bool, perm string, unlocked bool) (ipc.LinkView, error) {
	var p core.Permission
	if perm != "" {
		var err error
		if p, err = permission(perm); err != nil {
			return ipc.LinkView{}, err
		}
	}
	return a.result(ctx)(a.d.Links().DecideFor(ctx, sessionID, num, accept, p, authority(unlocked)))
}
