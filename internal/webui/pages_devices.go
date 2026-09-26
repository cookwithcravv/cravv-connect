package webui

import (
	"context"
	"regexp"
	"strings"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// addDevices is the Devices page: paired machines, pause, resume, unpair
// and pairing a new device. Pairing needs the password (the daemon gates
// pair.start, pair.await, join.start and pair.finalize).
func addDevices(r *Registry) {
	r.AddPage(Page{Path: "/devices", Title: "Devices", Template: "devices.html", Load: loadDevices})
	peerAction := func(path, method, done string) {
		r.AddAction(Action{Path: path, Back: "/devices", Run: func(ctx context.Context, rq *Request) (Reply, error) {
			alias := rq.Form("alias")
			if err := rq.Call(ctx, method, ipc.AliasParams{Alias: alias}, nil); err != nil {
				return Reply{}, err
			}
			return Reply{Notice: done + " " + alias + "."}, nil
		}})
	}
	peerAction("/devices/pause", ipc.MethodPeerPause, "Paused")
	peerAction("/devices/resume", ipc.MethodPeerResume, "Resumed")
	peerAction("/devices/unpair", ipc.MethodPeerUnpair, "Unpaired")
	r.AddAction(Action{Path: "/devices/pair", Back: "/devices", Run: pairStart})
	r.AddAction(Action{Path: "/devices/pair/wait", Back: "/devices", Run: pairWait})
	r.AddAction(Action{Path: "/devices/join", Back: "/devices", Run: joinStart})
	r.AddAction(Action{Path: "/devices/pair/finish", Back: "/devices", Run: pairFinish})
}

func loadDevices(ctx context.Context, rq *Request) (any, error) {
	var r ipc.PeerListResult
	if err := rq.Call(ctx, ipc.MethodMachines, nil, &r); err != nil {
		return nil, err
	}
	return r, nil
}

// pairCode is the step that shows the bind code and waits.
type pairCode struct {
	PendingID string
	Code      string
}

// pairName is the step that names the new device.
type pairName struct {
	PendingID string
	MachineID string
	Suggested string
}

func pairStart(ctx context.Context, rq *Request) (Reply, error) {
	var start ipc.PairStartResult
	if err := rq.WithPassword(ctx, func() error { return rq.Call(ctx, ipc.MethodPairStart, nil, &start) }); err != nil {
		return Reply{}, err
	}
	return Reply{Template: "pair.html", Title: "Pair a new device", Data: pairCode{PendingID: start.PendingID, Code: start.Code}}, nil
}

// pairWait blocks until the other device joins with the code.
func pairWait(ctx context.Context, rq *Request) (Reply, error) {
	var p ipc.PendingPeerResult
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: rq.Form("pending_id")}, &p)
	}); err != nil {
		return Reply{}, err
	}
	return nameStep(p), nil
}

func joinStart(ctx context.Context, rq *Request) (Reply, error) {
	var p ipc.PendingPeerResult
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: rq.Form("code")}, &p)
	}); err != nil {
		return Reply{}, err
	}
	return nameStep(p), nil
}

func nameStep(p ipc.PendingPeerResult) Reply {
	return Reply{Template: "pairname.html", Title: "Name the new device", Data: pairName{
		PendingID: p.PendingID, MachineID: cleanLine(p.MachineID), Suggested: suggestAlias(p.SuggestedName),
	}}
}

func pairFinish(ctx context.Context, rq *Request) (Reply, error) {
	var res ipc.PairFinalizeResult
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: rq.Form("pending_id"), Alias: rq.Form("alias")}, &res)
	}); err != nil {
		return Reply{}, err
	}
	return Reply{To: "/devices", Notice: "Paired with " + res.Alias + ". Its sessions can now ask to link with yours; you decide each link."}, nil
}

var aliasUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// suggestAlias turns the name the other device suggests (chosen by that
// device) into a valid local alias, as the CLI does.
func suggestAlias(s string) string {
	s = aliasUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 24 {
		s = strings.TrimRight(s[:24], "-")
	}
	if s == "" {
		return "peer"
	}
	return s
}
