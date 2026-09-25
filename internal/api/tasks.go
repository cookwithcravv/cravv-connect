package api

import (
	"context"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

func (h *handlers) registerTasks(s *ipc.Server) {
	s.Register(ipc.MethodTaskCreate, ipc.Typed(h.taskCreate), ipc.GateSession)
	s.Register(ipc.MethodTaskGet, ipc.Typed(h.taskByID(h.p.Tasks.Get)), ipc.GateSession)
	s.Register(ipc.MethodTaskClaim, ipc.Typed(h.taskByID(h.p.Tasks.Claim)), ipc.GateSession)
	s.Register(ipc.MethodTaskCancel, ipc.Typed(h.taskByID(h.p.Tasks.Cancel)), ipc.GateSession)
	s.Register(ipc.MethodTaskUpdate, ipc.Typed(h.taskUpdate), ipc.GateSession)
	s.Register(ipc.MethodTaskComplete, ipc.Typed(h.taskComplete), ipc.GateSession)
	s.Register(ipc.MethodTaskFail, ipc.Typed(h.taskFail), ipc.GateSession)
	s.Register(ipc.MethodApprovalsList, ipc.Typed(h.approvalsList), ipc.GateUnlock)
	s.Register(ipc.MethodApprovalsDecide, ipc.Typed(h.approvalsDecide), ipc.GateUnlock)
}

func (h *handlers) taskCreate(ctx context.Context, cs *ipc.ConnState, p ipc.TaskCreateParams) (any, error) {
	if err := required("to", p.To); err != nil {
		return nil, err
	}
	if err := required("instructions", p.Instructions); err != nil {
		return nil, err
	}
	if len(p.Instructions) > core.MaxTextBytes {
		return nil, core.ErrTooLarge
	}
	id, err := h.p.Tasks.Create(ctx, cs.Session(), cs.ProjectDir(), p.To, p.Instructions, p.FilePaths)
	if err != nil {
		return nil, err
	}
	return ipc.TaskCreateResult{TaskID: id}, nil
}

// taskByID adapts the task operations that take only an ID.
func (h *handlers) taskByID(op func(ctx context.Context, session, id string) (store.Task, error)) func(context.Context, *ipc.ConnState, ipc.TaskIDParams) (any, error) {
	return func(ctx context.Context, cs *ipc.ConnState, p ipc.TaskIDParams) (any, error) {
		if err := required("task_id", p.TaskID); err != nil {
			return nil, err
		}
		return h.taskResult(ctx)(op(ctx, cs.Session(), p.TaskID))
	}
}

func (h *handlers) taskUpdate(ctx context.Context, cs *ipc.ConnState, p ipc.TaskUpdateParams) (any, error) {
	if err := required("task_id", p.TaskID); err != nil {
		return nil, err
	}
	if err := required("note", p.Note); err != nil {
		return nil, err
	}
	if len(p.Note) > core.MaxTextBytes {
		return nil, core.ErrTooLarge
	}
	return h.taskResult(ctx)(h.p.Tasks.Update(ctx, cs.Session(), p.TaskID, p.Note))
}

func (h *handlers) taskComplete(ctx context.Context, cs *ipc.ConnState, p ipc.TaskCompleteParams) (any, error) {
	if err := required("task_id", p.TaskID); err != nil {
		return nil, err
	}
	if len(p.Result) > core.MaxTextBytes {
		return nil, core.ErrTooLarge
	}
	return h.taskResult(ctx)(h.p.Tasks.Complete(ctx, cs.Session(), cs.ProjectDir(), p.TaskID, p.Result, p.FilePaths))
}

func (h *handlers) taskFail(ctx context.Context, cs *ipc.ConnState, p ipc.TaskFailParams) (any, error) {
	if err := required("task_id", p.TaskID); err != nil {
		return nil, err
	}
	if len(p.Reason) > core.MaxTextBytes {
		return nil, core.ErrTooLarge
	}
	return h.taskResult(ctx)(h.p.Tasks.Fail(ctx, cs.Session(), p.TaskID, p.Reason))
}

func (h *handlers) taskResult(ctx context.Context) func(store.Task, error) (any, error) {
	return func(t store.Task, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return h.taskView(ctx, t), nil
	}
}

func (h *handlers) approvalsList(ctx context.Context, _ *ipc.ConnState, _ ipc.Empty) (any, error) {
	tasks, err := h.p.Tasks.Approvals(ctx)
	if err != nil {
		return nil, err
	}
	out := ipc.ApprovalsListResult{Tasks: make([]ipc.ApprovalView, 0, len(tasks))}
	for _, t := range tasks {
		out.Tasks = append(out.Tasks, h.approvalView(ctx, t))
	}
	return out, nil
}

func (h *handlers) approvalsDecide(ctx context.Context, cs *ipc.ConnState, p ipc.ApprovalsDecideParams) (any, error) {
	if err := required("task_id", p.TaskID); err != nil {
		return nil, err
	}
	return nil, h.p.Tasks.Decide(ctx, p.TaskID, p.Approve, cs.Unlocked())
}
