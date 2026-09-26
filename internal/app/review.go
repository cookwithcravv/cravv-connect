package app

import (
	"context"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
)

// review adapts the daemon's ReviewService to api.ReviewPort.
type review struct{ d *daemon.Daemon }

func (a review) List(ctx context.Context, sessionID string) ([]ipc.ReviewItemView, error) {
	items, err := a.d.Review().Pending(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.ReviewItemView, 0, len(items))
	for _, it := range items {
		v := ipc.ReviewItemView{Item: it.ID, Kind: "link", Link: it.Link.Num, Machine: it.Alias, Session: it.Link.RemoteName}
		var body []string
		if it.Task == nil {
			v.Permission = string(it.Link.Proposed)
			if it.Link.RemotePurpose != "" {
				body = append(body, "purpose: "+it.Link.RemotePurpose)
			}
			if it.Link.Note != "" {
				body = append(body, "note: "+it.Link.Note)
			}
		} else {
			v.Kind, v.Permission = "task", string(it.Link.PermissionIn)
			body = append(body, it.Task.Instructions)
		}
		if len(body) > 0 {
			v.Wrapped = present.Wrap(present.Item{Alias: it.Alias, Session: it.Link.RemoteName, Link: it.Link.Num, ID: it.ID, Kind: v.Kind, Body: strings.Join(body, "\n")})
		}
		out = append(out, v)
	}
	return out, nil
}

func (a review) Decide(ctx context.Context, sessionID string, p ipc.ReviewDecideParams) (ipc.ReviewDecideResult, error) {
	if err := daemon.ValidReviewItem(p.Item); err != nil {
		return ipc.ReviewDecideResult{}, err
	}
	ans := daemon.DecisionAnswer{Accept: p.Accept}
	if p.Permission != "" {
		perm, err := permission(p.Permission)
		if err != nil {
			return ipc.ReviewDecideResult{}, err
		}
		ans.Permission = perm
	}
	var dec daemon.Decider = daemon.AnswerDecider{Answer: ans}
	if p.Code != "" {
		dec = daemon.CodeDecider{Codes: a.d.Codes(), Item: p.Item, Code: p.Code, Answer: ans}
	}
	l, t, err := a.d.Review().Decide(ctx, sessionID, p.Item, dec)
	if err != nil {
		return ipc.ReviewDecideResult{}, err
	}
	res := ipc.ReviewDecideResult{Item: p.Item, Link: l.Num}
	switch {
	case t != nil && t.State == core.TaskQueued:
		res.Outcome = "approved"
	case t != nil:
		res.Outcome = "denied"
	case l.State == "active":
		res.Outcome, res.Permission = "accepted", string(l.PermissionIn)
	default:
		res.Outcome = "rejected"
	}
	return res, nil
}

func (a review) ShowCode(ctx context.Context, sessionID, item string) error {
	if err := daemon.ValidReviewItem(item); err != nil {
		return err
	}
	return a.d.Review().ShowCode(ctx, sessionID, item)
}
