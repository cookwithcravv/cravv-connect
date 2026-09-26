package daemon

import (
	"context"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// Failure reasons of tasks a managed session did not finish.
const (
	ReasonRateLimited   = "rate_limited"
	ReasonRunTimeout    = "run_timeout"
	ReasonRunFailed     = "run_failed"
	ReasonNoResult      = "no_result"
	ReasonFolderRefused = "folder_refused"
	ReasonStopped       = "stopped"
	ReasonInterrupted   = "interrupted"
)

// FailQueued fails a queued (not yet claimed) inbound task of session and
// tells its sender. The SessionHost uses it for a task it refuses to run
// (rate_limited, folder_refused).
func (s *TaskService) FailQueued(ctx context.Context, session, id, reason string) error {
	t, err := s.inboundTask(ctx, session, id)
	if err != nil {
		return err
	}
	_, err = s.failTasksFrom(ctx, []store.Task{t}, []core.TaskState{core.TaskQueued}, reason, true)
	return err
}

// FailClaimedBy fails every task session has claimed and not finished,
// and tells their senders (a run that ended without an answer, or a daemon
// that restarted during a run).
func (s *TaskService) FailClaimedBy(ctx context.Context, session, reason string) error {
	return s.failClaimed(ctx, store.TaskFilter{Direction: store.TaskInbound, States: claimedStates, ClaimedBy: session}, reason)
}
