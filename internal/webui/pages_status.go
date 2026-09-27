package webui

import (
	"context"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// addStatus is the Status page: machine status and the kill switch.
// Turning the kill switch on needs nothing; resuming needs the password.
func addStatus(r *Registry) {
	r.AddPage(Page{Path: "/status", Title: "Status", Template: "status.html", Load: loadStatus})
	r.AddAction(Action{Path: "/status/kill", Back: "/status", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		if err := rq.Call(ctx, ipc.MethodKill, nil, nil); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: "The kill switch is on. Every link is closed."}, nil
	}})
	r.AddAction(Action{Path: "/status/resume", Back: "/status", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		if err := rq.WithPassword(ctx, func(c Conn) error { return c.Call(ctx, ipc.MethodResume, nil, nil) }); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: "Resumed. Links must be requested again."}, nil
	}})
}

func loadStatus(ctx context.Context, rq *Request) (any, error) {
	var st ipc.StatusResult
	if err := rq.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
		return nil, err
	}
	return st, nil
}
