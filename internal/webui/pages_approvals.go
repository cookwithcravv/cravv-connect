package webui

import (
	"context"
	"fmt"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
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
		if err := rq.WithPassword(ctx, func(c Conn) error {
			return c.Call(ctx, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: n, Accept: true, Permission: rq.Form("permission")}, &v)
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
	r.AddAction(Action{Path: "/approvals/tasks", Back: "/approvals", Run: showTasks})
	r.AddAction(Action{Path: "/approvals/task", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		id, approve := rq.Form("task_id"), rq.Form("decision") == "approve"
		if err := rq.WithPassword(ctx, func(c Conn) error {
			return c.Call(ctx, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: id, Approve: approve}, nil)
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

// loadApprovals shows the link requests. Tasks need the password, so the
// page only offers the form that shows them (showTasks).
func loadApprovals(ctx context.Context, rq *Request) (any, error) {
	d := approvalsData{Permissions: permissions, TasksLocked: true}
	var links ipc.LinksResult
	if err := rq.Call(ctx, ipc.MethodLinks, nil, &links); err != nil {
		return nil, err
	}
	for _, l := range links.Links {
		if isRequest(l) {
			d.Requests = append(d.Requests, newLinkRow(l))
		}
	}
	return d, nil
}

// showTasks answers the password form with the Approvals page including
// the tasks waiting for approval. They appear only in this answer; loading
// the page again hides them.
func showTasks(ctx context.Context, rq *Request) (Reply, error) {
	var tasks ipc.ApprovalsListResult
	if err := rq.WithPassword(ctx, func(c Conn) error {
		return c.Call(ctx, ipc.MethodApprovalsList, nil, &tasks)
	}); err != nil {
		return Reply{}, err
	}
	data, err := loadApprovals(ctx, rq)
	if err != nil {
		return Reply{}, err
	}
	d := data.(approvalsData)
	d.TasksLocked = false
	for _, t := range tasks.Tasks {
		d.Tasks = append(d.Tasks, taskRow{ApprovalView: t, PreviewText: peerLines(t.Preview), FullText: peerLines(t.Full), Truncated: t.Preview != t.Full})
	}
	return Reply{Template: "approvals.html", Title: "Approvals", Data: d}, nil
}
