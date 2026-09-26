package webui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// addManaged is the Managed page: managed sessions other machines started
// here (open, close) and the offer rules that allow them. Editing and
// removing a rule needs the password (v2 spec 10); closing a session is a
// cut-off and needs nothing. The daemon cannot open a terminal, so Open
// shows the command that opens the session's conversation.
func addManaged(r *Registry) {
	r.AddPage(Page{Path: "/managed", Title: "Managed", Template: "managed.html", Load: loadManaged})
	r.AddAction(Action{Path: "/managed/close", Back: "/managed", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		name := rq.Form("name")
		if err := rq.Call(ctx, ipc.MethodManagedClose, ipc.ManagedNameParams{Name: name}, nil); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: "Closed " + name + "; its link closed too."}, nil
	}})
	r.AddAction(Action{Path: "/managed/offers/set", Back: "/managed", Run: setOffer})
	r.AddAction(Action{Path: "/managed/offers/remove", Back: "/managed", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		p := ipc.OfferRemoveParams{Machine: rq.Form("machine"), Label: rq.Form("label")}
		if err := rq.WithPassword(ctx, func(c Conn) error {
			return c.Call(ctx, ipc.MethodOffersRemove, p, nil)
		}); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Removed offer %s to %s; its managed sessions closed.", p.Label, p.Machine)}, nil
	}})
}

// offerRow is an offer with its limits in words.
type offerRow struct {
	ipc.OfferView
	Limits string
}

type managedData struct {
	Sessions    []ipc.ManagedView
	Open        string // the session whose open command is shown
	Offers      []offerRow
	Peers       []ipc.PeerView
	Permissions []core.Permission
	Modes       []core.RunMode
}

func loadManaged(ctx context.Context, rq *Request) (any, error) {
	d := managedData{
		Open:        rq.Query("open"),
		Permissions: []core.Permission{core.PermMessages, core.PermTasksAuto},
		Modes:       []core.RunMode{core.RunReadOnly, core.RunEditInFolder, core.RunShell},
	}
	var sessions ipc.ManagedListResult
	if err := rq.Call(ctx, ipc.MethodManagedList, nil, &sessions); err != nil {
		return nil, err
	}
	d.Sessions = sessions.Sessions
	if d.Open != "" && !core.ValidSessionName(d.Open) {
		d.Open = ""
	}
	var offers ipc.OffersListResult
	if err := rq.Call(ctx, ipc.MethodOffersList, ipc.OffersListParams{}, &offers); err != nil {
		return d, err
	}
	for _, o := range offers.Offers {
		d.Offers = append(d.Offers, offerRow{OfferView: o, Limits: fmt.Sprintf("%d open, %d runs an hour, %d a day, run %s, idle %s",
			o.MaxConcurrent, o.RunsPerHour, o.RunsPerDay, time.Duration(o.RunTimeoutS)*time.Second, time.Duration(o.IdleTimeoutS)*time.Second)})
	}
	var peers ipc.PeerListResult
	if err := rq.Call(ctx, ipc.MethodMachines, nil, &peers); err != nil {
		return d, err
	}
	d.Peers = peers.Peers
	return d, nil
}

// formInt reads an optional whole number field (empty is 0: the default).
func formInt(rq *Request, name string) (int, error) {
	v := rq.Form(name)
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s must be a whole number", ipc.ErrBadRequest, name)
	}
	return n, nil
}

// setOffer creates or changes an offer. Run mode shell needs the word
// shell typed into the confirmation field as well as the password.
func setOffer(ctx context.Context, rq *Request) (Reply, error) {
	p := ipc.OfferSetParams{
		Machine: rq.Form("machine"), Label: rq.Form("label"), Folder: rq.Form("folder"),
		Permission: rq.Form("permission"), RunMode: rq.Form("run_mode"), ShellConfirm: rq.Form("shell_confirm"),
	}
	var err error
	nums := []struct {
		field string
		dst   *int
		scale int
	}{
		{"max_concurrent", &p.MaxConcurrent, 1}, {"runs_per_hour", &p.RunsPerHour, 1}, {"runs_per_day", &p.RunsPerDay, 1},
		{"run_timeout_m", &p.RunTimeoutS, 60}, {"idle_timeout_m", &p.IdleTimeoutS, 60},
	}
	for _, n := range nums {
		v, ferr := formInt(rq, n.field)
		if ferr != nil {
			return Reply{}, ferr
		}
		*n.dst = v * n.scale
	}
	var v ipc.OfferView
	if err = rq.WithPassword(ctx, func(c Conn) error {
		return c.Call(ctx, ipc.MethodOffersSet, p, &v)
	}); err != nil {
		return Reply{}, err
	}
	return Reply{Notice: fmt.Sprintf("Offer %s to %s: %s (%s, %s). On %s, a chat connects to new:%s on this machine.",
		v.Label, v.Machine, v.Folder, v.RunMode, v.Permission, v.Machine, v.Label)}, nil
}
