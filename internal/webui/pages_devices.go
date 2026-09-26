package webui

import (
	"context"
	"errors"
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
	Flow string
	Code string
}

// pairName is the step that names the new device.
type pairName struct {
	Flow      string
	MachineID string
	Alias     string
}

// pairStart starts a pairing on a connection of its own (StartFlow),
// unlocked with the password.
func pairStart(ctx context.Context, rq *Request) (Reply, error) {
	var start ipc.PairStartResult
	flow, err := rq.StartFlow(ctx, func(c Conn) (string, error) {
		err := c.Call(ctx, ipc.MethodPairStart, nil, &start)
		return start.PendingID, err
	})
	if err != nil {
		return Reply{}, err
	}
	return Reply{Template: "pair.html", Title: "Pair a new device", Data: pairCode{Flow: flow, Code: start.Code}}, nil
}

// pairWait blocks until the other device joins with the code. It needs no
// password: it runs on the pairing's own connection, which only this
// browser session can name. If it fails, the pairing ends.
func pairWait(ctx context.Context, rq *Request) (Reply, error) {
	var p ipc.PendingPeerResult
	flow, err := rq.FlowStep(ctx, false, func(c Conn, pending string) error {
		return c.Call(ctx, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: pending}, &p)
	})
	if err != nil {
		rq.EndFlow(flow)
		return Reply{}, err
	}
	return nameStep(flow, p.MachineID, suggestAlias(p.SuggestedName)), nil
}

func joinStart(ctx context.Context, rq *Request) (Reply, error) {
	var p ipc.PendingPeerResult
	flow, err := rq.StartFlow(ctx, func(c Conn) (string, error) {
		err := c.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: rq.Form("code")}, &p)
		return p.PendingID, err
	})
	if err != nil {
		return Reply{}, err
	}
	return nameStep(flow, p.MachineID, suggestAlias(p.SuggestedName)), nil
}

func nameStep(flow, machineID, alias string) Reply {
	return Reply{Template: "pairname.html", Title: "Name the new device", Data: pairName{
		Flow: flow, MachineID: cleanLine(machineID), Alias: alias,
	}}
}

// pairFinish names the new device and finishes the pairing. It needs the
// password again; an error keeps the naming step so it can be retried.
func pairFinish(ctx context.Context, rq *Request) (Reply, error) {
	var res ipc.PairFinalizeResult
	alias := rq.Form("alias")
	flow, err := rq.FlowStep(ctx, true, func(c Conn, pending string) error {
		return c.Call(ctx, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: pending, Alias: alias}, &res)
	})
	if errors.Is(err, errFlowEnded) {
		return Reply{}, err
	}
	if err != nil {
		return nameStep(flow, rq.Form("machine"), alias), err
	}
	rq.EndFlow(flow)
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
