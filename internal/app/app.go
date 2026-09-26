// Package app is the composition seam between the daemon and the IPC API.
//
// Every call into *daemon.Daemon made by Phase E lives in this package, so a
// change to a daemon signature is fixed here and nowhere else; internal/api
// and internal/ipc never import internal/daemon.
package app

import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/cravv/cravv-connect/internal/store"
)

// Ports adapts a daemon to the API ports. Adapters call the daemon's accessors
// on every use (several services are swapped by ResetIdentity), so
// construction never touches d.
func Ports(d *daemon.Daemon) api.Ports {
	return api.Ports{
		Sessions: sessions{d}, Shared: shared{d}, Discovery: discovery{d}, Links: links{d}, Review: review{d}, Chat: chat{d}, Inbox: inbox{d}, Tasks: tasks{d}, Files: files{d},
		Peers: peers{d}, Pairing: pairing{d}, Control: control{d}, Status: status{d},
		Audit: auditReader{d}, Hook: hook{d}, Auth: guard{d},
	}
}

type sessions struct{ d *daemon.Daemon }

func (a sessions) Register(ctx context.Context, agent, dir string) (string, error) {
	return a.d.Sessions().Register(ctx, agent, dir)
}
func (a sessions) Disconnect(ctx context.Context, name string) error {
	return a.d.Sessions().Disconnect(ctx, name)
}

// chat sends on an active link of the shared session (core.ErrLinkClosed
// at once otherwise) through the outbound queue, which refuses peers paused
// by this machine (core.ErrPaused). The outbound queue accepts envelopes
// while the kill switch is on; the IPC kill gate refuses chat.send before it
// gets here.
type chat struct{ d *daemon.Daemon }

func (a chat) Send(ctx context.Context, sessionID string, link int64, text string) (string, error) {
	l, err := a.d.Links().Active(ctx, sessionID, link)
	if err != nil {
		return "", err
	}
	return a.d.Outbound().SendEnvelope(ctx, l.Peer, core.KindChat, l.ID, core.ChatBody{Text: text})
}

type inbox struct{ d *daemon.Daemon }

// inboxView maps a rendered daemon entry to its wire view.
func inboxView(e daemon.InboxEntry) ipc.InboxView {
	return ipc.InboxView{
		Seq: e.Item.Seq, ID: e.Item.MsgID, From: e.Alias, Session: present.CleanAttr(e.Session), Link: e.Link, Kind: e.Kind,
		TaskID: e.Item.TaskID, FileID: e.FileID, Path: e.Path, Wrapped: e.Wrapped, At: e.Item.ReceivedAt,
	}
}

func views(entries []daemon.InboxEntry, err error) ([]ipc.InboxView, error) {
	if err != nil {
		return nil, err
	}
	out := make([]ipc.InboxView, len(entries))
	for i, e := range entries {
		out[i] = inboxView(e)
	}
	return out, nil
}

func (a inbox) Check(ctx context.Context, session string, limit int) ([]ipc.InboxView, error) {
	return views(a.d.Inbox().Check(ctx, session, limit))
}
func (a inbox) Wait(ctx context.Context, session string, timeout time.Duration) ([]ipc.InboxView, error) {
	return views(a.d.Inbox().Wait(ctx, session, timeout))
}

type tasks struct{ d *daemon.Daemon }

func (a tasks) Create(ctx context.Context, session, dir string, link int64, instr string, paths []string) (string, error) {
	return a.d.Tasks().Create(ctx, session, dir, link, instr, paths)
}
func (a tasks) Get(ctx context.Context, session, id string) (store.Task, error) {
	return a.d.Tasks().Get(ctx, session, id)
}
func (a tasks) Claim(ctx context.Context, session, id string) (store.Task, error) {
	return a.d.Tasks().Claim(ctx, session, id)
}
func (a tasks) Update(ctx context.Context, session, id, note string) (store.Task, error) {
	return a.d.Tasks().Update(ctx, session, id, note)
}
func (a tasks) Complete(ctx context.Context, session, dir, id, result string, paths []string) (store.Task, error) {
	return a.d.Tasks().Complete(ctx, session, dir, id, result, paths)
}
func (a tasks) Fail(ctx context.Context, session, id, reason string) (store.Task, error) {
	return a.d.Tasks().Fail(ctx, session, id, reason)
}
func (a tasks) Cancel(ctx context.Context, session, id string) (store.Task, error) {
	return a.d.Tasks().Cancel(ctx, session, id)
}
func (a tasks) Approvals(ctx context.Context) ([]store.Task, error) {
	list, err := a.d.Tasks().Approvals(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]store.Task, len(list))
	for i, ap := range list {
		out[i] = ap.Task
	}
	return out, nil
}
func (a tasks) Decide(ctx context.Context, id string, approve, unlocked bool) error {
	return a.d.Tasks().Decide(ctx, id, approve, authority(unlocked))
}

type files struct{ d *daemon.Daemon }

func (a files) Send(ctx context.Context, sessionID string, link int64, dir, path string) (core.FileRef, error) {
	l, err := a.d.Links().Active(ctx, sessionID, link)
	if err != nil {
		return core.FileRef{}, err
	}
	return a.d.Files().SendFile(ctx, l, dir, path, "")
}

// List never hands a content key to the API; the daemon already zeroes it and
// this adapter does too, so no view can leak one.
func (a files) List(ctx context.Context) ([]store.FileRecord, error) {
	list, err := a.d.Files().List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Key = nil
	}
	return list, nil
}

type peers struct{ d *daemon.Daemon }

func (a peers) ByAlias(ctx context.Context, alias string) (store.Peer, error) {
	p, _, err := a.d.Peers().Resolve(ctx, alias)
	return p, err
}

// ByID uses Resolve, which accepts a full machine ID in place of an alias.
func (a peers) ByID(ctx context.Context, id core.MachineID) (store.Peer, error) {
	p, _, err := a.d.Peers().Resolve(ctx, string(id))
	return p, err
}
func (a peers) Pause(ctx context.Context, alias string) error  { return a.d.Peers().Pause(ctx, alias) }
func (a peers) Resume(ctx context.Context, alias string) error { return a.d.Peers().Resume(ctx, alias) }
func (a peers) Unpair(ctx context.Context, alias string) error { return a.d.Peers().Unpair(ctx, alias) }
func (a peers) Rename(ctx context.Context, alias, newAlias string) error {
	return a.d.Peers().SetAlias(ctx, alias, newAlias)
}

type pairing struct{ d *daemon.Daemon }

func proposal(p daemon.Proposal, err error) (ipc.PendingPeerResult, error) {
	if err != nil {
		return ipc.PendingPeerResult{}, err
	}
	return ipc.PendingPeerResult{PendingID: p.PendingID, SuggestedName: p.SuggestedName, MachineID: string(p.MachineID)}, nil
}

func (a pairing) Start(ctx context.Context, unlocked bool) (ipc.PairStartResult, error) {
	id, code, err := a.d.Pairing().Start(ctx, unlocked)
	if err != nil {
		return ipc.PairStartResult{}, err
	}
	return ipc.PairStartResult{PendingID: id, Code: code}, nil
}
func (a pairing) Await(ctx context.Context, id string) (ipc.PendingPeerResult, error) {
	return proposal(a.d.Pairing().Await(ctx, id))
}
func (a pairing) Join(ctx context.Context, code string, unlocked bool) (ipc.PendingPeerResult, error) {
	return proposal(a.d.Pairing().Join(ctx, code, unlocked))
}
func (a pairing) Finalize(ctx context.Context, id, alias string, unlocked bool) (string, error) {
	return a.d.Pairing().Finalize(ctx, id, alias, unlocked)
}

type control struct{ d *daemon.Daemon }

func (a control) Killed() bool                   { return a.d.Kill().Killed() }
func (a control) Kill(ctx context.Context) error { return a.d.Kill().Kill(ctx) }
func (a control) Resume(ctx context.Context, unlocked bool) error {
	return a.d.Kill().Resume(ctx, unlocked)
}
func (a control) ResetIdentity(ctx context.Context, unlocked bool) error {
	return a.d.ResetIdentity(ctx, unlocked)
}
func (a control) AddAllowPath(ctx context.Context, dir string, unlocked bool) error {
	return a.d.AllowPaths().Add(ctx, dir, unlocked)
}

type status struct{ d *daemon.Daemon }

func (a status) Status(ctx context.Context) (ipc.StatusResult, error) {
	s, err := a.d.Status().Status(ctx)
	if err != nil {
		return ipc.StatusResult{}, err
	}
	out := ipc.StatusResult{
		Version: s.Version, MachineID: string(s.MachineID), DeviceName: s.DeviceName, RelayURL: s.RelayURL,
		RelayConnected: s.RelayConnected, Killed: s.Killed, Sessions: s.Sessions,
		OutboxPending: s.OutboxPending, OutboxHeld: s.OutboxHeld, InboxUnread: s.InboxUnread,
		PendingApprovals: s.PendingApprovals, Errors: s.Errors,
		Peers: make([]ipc.PeerView, 0, len(s.Peers)),
	}
	for _, p := range s.Peers {
		v := api.PeerViewOf(p, s.RelayConnected)
		v.Online = s.Online[p.MachineID]
		v.LastSeen = s.LastSeen[p.MachineID]
		out.Peers = append(out.Peers, v)
	}
	return out, nil
}

type auditReader struct{ d *daemon.Daemon }

func (a auditReader) Read(_ context.Context, limit int) ([]audit.Event, error) {
	return audit.ReadEvents(a.d.AuditPath(), limit)
}

type hook struct{ d *daemon.Daemon }

// Check answers for the shared session of the chat running the hook.
func (a hook) Check(ctx context.Context, q ipc.HookCountsParams) (ipc.HookCountsResult, error) {
	ans, err := a.d.Hooks().Check(ctx, daemon.HookQuery{AgentSession: q.SessionID, Cwd: q.Cwd, Event: q.Event, StopHookActive: q.StopHookActive})
	if err != nil {
		return ipc.HookCountsResult{}, err
	}
	return ipc.HookCountsResult{
		Notice: ans.Notice, Unread: ans.Counts.Unhandled(), Approvals: ans.Counts.Requests + ans.Counts.Approvals,
		Block: ans.Block, Reason: ans.Reason,
	}, nil
}

func (a hook) Bind(_ context.Context, agentSession, sessionID string) {
	a.d.Hooks().Bind(agentSession, sessionID)
}

type guard struct{ d *daemon.Daemon }

func (a guard) Check(password string) error { return a.d.Guard().Check(password) }
