package core

// TaskState is the lifecycle state of a task.
type TaskState string

const (
	TaskSent             TaskState = "sent" // sender-side only
	TaskAwaitingApproval TaskState = "awaiting_approval"
	TaskQueued           TaskState = "queued"
	TaskSeen             TaskState = "seen" // sender-side only: the receiving session's inbox returned it
	TaskClaimed          TaskState = "claimed"
	TaskRunning          TaskState = "running"
	TaskDone             TaskState = "done"
	TaskFailed           TaskState = "failed"
	TaskCancelled        TaskState = "cancelled"
	TaskRejected         TaskState = "rejected"
	TaskExpired          TaskState = "expired"
)

// AllTaskStates lists every defined state, in lifecycle order.
var AllTaskStates = []TaskState{
	TaskSent, TaskAwaitingApproval, TaskQueued, TaskSeen, TaskClaimed, TaskRunning,
	TaskDone, TaskFailed, TaskCancelled, TaskRejected, TaskExpired,
}

// Terminal reports whether no further transition is possible.
func (s TaskState) Terminal() bool {
	switch s {
	case TaskDone, TaskFailed, TaskCancelled, TaskRejected, TaskExpired:
		return true
	}
	return false
}

// transitions is the full table of allowed moves. Anything absent is refused,
// including staying in the same state.
var transitions = map[TaskState]map[TaskState]bool{
	// Sender-side mirror: the sender adopts whatever state task.update reports.
	TaskSent: {
		TaskAwaitingApproval: true, TaskQueued: true, TaskSeen: true, TaskClaimed: true, TaskRunning: true,
		TaskDone: true, TaskFailed: true, TaskCancelled: true, TaskRejected: true, TaskExpired: true,
	},
	// A closed link fails every unfinished task on it (failed: link_closed).
	TaskAwaitingApproval: {TaskQueued: true, TaskRejected: true, TaskExpired: true, TaskCancelled: true, TaskFailed: true},
	TaskQueued:           {TaskClaimed: true, TaskExpired: true, TaskCancelled: true, TaskRejected: true, TaskFailed: true},
	TaskSeen: {
		TaskClaimed: true, TaskRunning: true, TaskDone: true, TaskFailed: true,
		TaskCancelled: true, TaskRejected: true, TaskExpired: true,
	},
	// An abandoned claim becomes failed; a claim is never returned to queued.
	TaskClaimed: {TaskRunning: true, TaskDone: true, TaskFailed: true, TaskCancelled: true},
	TaskRunning: {TaskDone: true, TaskFailed: true, TaskCancelled: true},
}

// CanTransition reports whether a task may move from one state to another.
func CanTransition(from, to TaskState) bool { return transitions[from][to] }
