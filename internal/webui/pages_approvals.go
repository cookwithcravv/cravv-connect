package webui

import (
	"context"
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// addApprovals is the Approvals page: link requests waiting for this side
// and tasks under tasks-ask links. Rejecting a link needs nothing;
// accepting one, and seeing or deciding tasks, need the password.
func addApprovals(r *Registry) {
	r.AddPage(Page{Path: "/approvals", Title: "Approvals", Template: "approvals.html", Load: loadApprovals})
	r.AddAction(Action{Path: "/approvals/link/accept", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		var v ipc.LinkView
		if err := rq.WithPassword(ctx, func() error {
			return rq.Call(ctx, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: n, Accept: true, Permission: rq.Form("permission")}, &v)
		}); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Accepted link %d: %s/%s may now use %s.", v.Link, v.Machine, v.RemoteSession, v.PermissionIn)}, nil
	}})
	r.AddAction(Action{Path: "/approvals/link/reject", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		if err := rq.Call(ctx, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: n}, nil); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Rejected link %d.", n)}, nil
	}})
	r.AddAction(Action{Path: "/approvals/unlock", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		if rq.HTTP.PostFormValue("password") == "" {
			return Reply{}, core.ErrAuthRequired
		}
		if err := rq.WithPassword(ctx, func() error { return nil }); err != nil {
			return Reply{}, err
		}
		return Reply{}, nil
	}})
	r.AddAction(Action{Path: "/approvals/task", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		id, approve := rq.Form("task_id"), rq.Form("decision") == "approve"
		if err := rq.WithPassword(ctx, func() error {
			return rq.Call(ctx, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: id, Approve: approve}, nil)
		}); err != nil {
			return Reply{}, err
		}
		if approve {
			return Reply{Notice: "Approved task " + id + "."}, nil
		}
		return Reply{Notice: "Denied task " + id + "."}, nil
	}})
}

// taskRow is a task waiting for approval. Preview and Full are the peer's
// instructions, cleaned line by line.
type taskRow struct {
	ipc.ApprovalView
	PreviewText string
	FullText    string
	Truncated   bool
}

type approvalsData struct {
	Requests    []linkRow
	Tasks       []taskRow
	TasksLocked bool
	Permissions []core.Permission
}

func loadApprovals(ctx context.Context, rq *Request) (any, error) {
	d := approvalsData{Permissions: permissions}
	var links ipc.LinksResult
	if err := rq.Call(ctx, ipc.MethodLinks, nil, &links); err != nil {
		return nil, err
	}
	for _, l := range links.Links {
		if isRequest(l) {
			d.Requests = append(d.Requests, newLinkRow(l))
		}
	}
	var tasks ipc.ApprovalsListResult
	err := rq.Call(ctx, ipc.MethodApprovalsList, nil, &tasks)
	if errors.Is(err, core.ErrAuthRequired) {
		d.TasksLocked = true
		return d, nil
	}
	if err != nil {
		return d, err
	}
	for _, t := range tasks.Tasks {
		d.Tasks = append(d.Tasks, taskRow{ApprovalView: t, PreviewText: peerLines(t.Preview), FullText: peerLines(t.Full), Truncated: t.Preview != t.Full})
	}
	return d, nil
}
