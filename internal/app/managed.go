package app

import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

func init() {
	for _, e := range []struct {
		err  error
		kind string
	}{
		{daemon.ErrBadOffer, ipc.KindBadRequest},
		{daemon.ErrBadFolder, ipc.KindBadRequest},
		{daemon.ErrShellNotConfirmed, ipc.KindBadRequest},
		{store.ErrOfferLabelTaken, ipc.KindBadRequest},
		{daemon.ErrManagedBusy, KindBusy},
		{daemon.ErrClaudeNotFound, ipc.KindBadRequest},
	} {
		ipc.RegisterErrorKind(e.err, e.kind)
	}
}

// ManagedPorts adapts the daemon's offer rules and SessionHost to the
// managed-session methods.
func ManagedPorts(d *daemon.Daemon) api.ManagedPorts {
	return api.ManagedPorts{Offers: offers{d}, Managed: managed{d}, Runs: runs{d}}
}

type offers struct{ d *daemon.Daemon }

func (a offers) view(ctx context.Context, o store.Offer) ipc.OfferView {
	alias := o.Peer.Short()
	if p, _, err := a.d.Peers().Resolve(ctx, string(o.Peer)); err == nil {
		alias = p.Alias
	}
	return ipc.OfferView{
		Machine: alias, Label: o.Label, Folder: o.Folder, Agent: o.Agent, Permission: string(o.Permission), RunMode: string(o.RunMode),
		MaxConcurrent: o.MaxConcurrent, IdleTimeoutS: int(o.IdleTimeout / time.Second), MaxTurnsPerRun: o.MaxTurnsPerRun,
		RunTimeoutS: int(o.RunTimeout / time.Second), RunsPerHour: o.RunsPerHour, RunsPerDay: o.RunsPerDay, UpdatedAt: o.UpdatedAt,
	}
}

func (a offers) List(ctx context.Context, machine string) ([]ipc.OfferView, error) {
	list, err := a.d.Offers().List(ctx, machine)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.OfferView, 0, len(list))
	for _, o := range list {
		out = append(out, a.view(ctx, o))
	}
	return out, nil
}

func (a offers) Set(ctx context.Context, p ipc.OfferSetParams, unlocked bool) (ipc.OfferView, error) {
	perm, err := core.ParsePermission(p.Permission)
	if err != nil {
		return ipc.OfferView{}, daemon.ErrBadPermission
	}
	var mode core.RunMode
	if p.RunMode != "" {
		if mode, err = core.ParseRunMode(p.RunMode); err != nil {
			return ipc.OfferView{}, daemon.ErrBadOffer
		}
	}
	o, err := a.d.Offers().Set(ctx, daemon.OfferInput{
		Peer: p.Machine, Label: p.Label, Folder: p.Folder, Agent: p.Agent, Permission: perm, RunMode: mode, ShellConfirm: p.ShellConfirm, Force: p.Force,
		MaxConcurrent: p.MaxConcurrent, IdleTimeout: time.Duration(p.IdleTimeoutS) * time.Second, MaxTurnsPerRun: p.MaxTurnsPerRun,
		RunTimeout: time.Duration(p.RunTimeoutS) * time.Second, RunsPerHour: p.RunsPerHour, RunsPerDay: p.RunsPerDay,
	}, authority(unlocked))
	if err != nil {
		return ipc.OfferView{}, err
	}
	return a.view(ctx, o), nil
}

func (a offers) Remove(ctx context.Context, machine, label string, unlocked bool) error {
	_, err := a.d.Offers().Remove(ctx, machine, label, authority(unlocked))
	return err
}

type managed struct{ d *daemon.Daemon }

func (a managed) List(ctx context.Context) ([]ipc.ManagedView, error) {
	list, err := a.d.Host().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.ManagedView, 0, len(list))
	for _, m := range list {
		v := ipc.ManagedView{
			Name: m.Session.Name, Machine: m.Alias, Offer: m.Offer.Label, Folder: m.Session.ProjectDir, RunMode: string(m.Offer.RunMode),
			Link: m.Link, State: "idle", Started: m.Managed.Started, LastActive: m.Managed.LastActive,
		}
		switch {
		case m.Live:
			v.State = "live"
		case m.Running:
			v.State = "running"
		}
		out = append(out, v)
	}
	return out, nil
}

func (a managed) Open(ctx context.Context, name string) (ipc.ManagedOpenResult, func(), error) {
	info, release, err := a.d.Host().Open(ctx, name)
	if err != nil {
		return ipc.ManagedOpenResult{}, nil, err
	}
	return ipc.ManagedOpenResult{Name: info.Name, Machine: info.Machine, Folder: info.Folder, Command: info.Command}, release, nil
}

func (a managed) Close(ctx context.Context, name string) error {
	return a.d.Host().CloseByName(ctx, name)
}

type runs struct{ d *daemon.Daemon }

func (a runs) Bind(ctx context.Context, token string, conn uint64) (api.RunBinding, error) {
	s, err := a.d.Host().BindRun(ctx, token, conn)
	if err != nil {
		return api.RunBinding{}, err
	}
	return api.RunBinding{ID: s.ID, Folder: s.ProjectDir, View: shared{a.d}.view(ctx, s)}, nil
}
