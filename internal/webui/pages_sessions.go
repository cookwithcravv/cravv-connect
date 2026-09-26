package webui

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// addSessions is the Sessions page: local shared sessions, their links, and
// the sessions each paired device shows this machine. Disconnect and
// restrict need nothing; asking for a link on a session's behalf needs the
// password (link.connect_as).
func addSessions(r *Registry) {
	r.AddPage(Page{Path: "/sessions", Title: "Sessions", Template: "sessions.html", Load: loadSessions})
	r.AddAction(Action{Path: "/sessions/connect", Back: "/sessions", Run: connectAs})
	r.AddAction(Action{Path: "/sessions/disconnect", Back: "/sessions", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		if err := rq.Call(ctx, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: n}, nil); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Disconnected link %d.", n)}, nil
	}})
	r.AddAction(Action{Path: "/sessions/restrict", Back: "/sessions", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		var v ipc.LinkView
		if err := rq.Call(ctx, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: n, Permission: rq.Form("permission")}, &v); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Link %d now allows %s.", v.Link, v.PermissionIn)}, nil
	}})
}

// remoteRow is a session another device shows this machine.
type remoteRow struct {
	ipc.RemoteSessionView
	Purpose string
}

type sessionsData struct {
	Local       []ipc.SharedSessionView
	Open        []string // local sessions that can ask for a link
	Links       []linkRow
	Requests    int
	Peers       []ipc.PeerView
	Machine     string
	Remote      []remoteRow
	Permissions []core.Permission
}

func loadSessions(ctx context.Context, rq *Request) (any, error) {
	d := sessionsData{Machine: rq.Query("machine"), Permissions: permissions}
	var local ipc.LocalSessionsResult
	if err := rq.Call(ctx, ipc.MethodSessionsLocal, nil, &local); err != nil {
		return nil, err
	}
	d.Local = local.Sessions
	for _, s := range local.Sessions {
		if s.State == string(core.SessionOpen) {
			d.Open = append(d.Open, s.Name)
		}
	}
	var links ipc.LinksResult
	if err := rq.Call(ctx, ipc.MethodLinks, nil, &links); err != nil {
		return d, err
	}
	for _, l := range links.Links {
		if isRequest(l) {
			d.Requests++
			continue
		}
		d.Links = append(d.Links, newLinkRow(l))
	}
	var peers ipc.PeerListResult
	if err := rq.Call(ctx, ipc.MethodMachines, nil, &peers); err != nil {
		return d, err
	}
	d.Peers = peers.Peers
	if d.Machine == "" {
		return d, nil
	}
	var remote ipc.SessionsListResult
	if err := rq.Call(ctx, ipc.MethodSessionsList, ipc.MachineParams{Machine: d.Machine}, &remote); err != nil {
		return d, err
	}
	d.Machine = remote.Machine
	for _, s := range remote.Sessions {
		d.Remote = append(d.Remote, remoteRow{RemoteSessionView: s, Purpose: peerText(s.Wrapped)})
	}
	return d, nil
}

func connectAs(ctx context.Context, rq *Request) (Reply, error) {
	session, machine, remote := rq.Form("session"), rq.Form("machine"), rq.Form("remote")
	target := machine + "/" + remote
	var v ipc.LinkView
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{
			Session: session, Target: target, Permission: rq.Form("permission"), Note: rq.Form("note"),
		}, &v)
	}); err != nil {
		return Reply{}, err
	}
	return Reply{Notice: fmt.Sprintf("Link %d: asked %s for a link from %s. The other side decides.", v.Link, target, session)}, nil
}
