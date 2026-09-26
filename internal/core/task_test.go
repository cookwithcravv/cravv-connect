package core

import "testing"

func TestTaskStateTerminal(t *testing.T) {
	terminal := map[TaskState]bool{TaskDone: true, TaskFailed: true, TaskCancelled: true, TaskRejected: true, TaskExpired: true}
	for _, s := range AllTaskStates {
		if s.Terminal() != terminal[s] {
			t.Errorf("%s.Terminal() = %v, want %v", s, s.Terminal(), terminal[s])
		}
	}
}

// TestCanTransitionExhaustive checks every (from, to) pair of defined states
// against the allowed list written out independently here.
func TestCanTransitionExhaustive(t *testing.T) {
	allowed := map[[2]TaskState]bool{}
	allow := func(from TaskState, tos ...TaskState) {
		for _, to := range tos {
			allowed[[2]TaskState{from, to}] = true
		}
	}
	allow(TaskSent, TaskAwaitingApproval, TaskQueued, TaskSeen, TaskClaimed, TaskRunning,
		TaskDone, TaskFailed, TaskCancelled, TaskRejected, TaskExpired)
	allow(TaskAwaitingApproval, TaskQueued, TaskRejected, TaskExpired, TaskCancelled, TaskFailed)
	allow(TaskQueued, TaskClaimed, TaskExpired, TaskCancelled, TaskRejected, TaskFailed)
	allow(TaskSeen, TaskClaimed, TaskRunning, TaskDone, TaskFailed, TaskCancelled, TaskRejected, TaskExpired)
	allow(TaskClaimed, TaskRunning, TaskDone, TaskFailed, TaskCancelled)
	allow(TaskRunning, TaskDone, TaskFailed, TaskCancelled)

	if len(AllTaskStates) != 11 {
		t.Fatalf("AllTaskStates has %d states, want 11", len(AllTaskStates))
	}
	for _, from := range AllTaskStates {
		for _, to := range AllTaskStates {
			want := allowed[[2]TaskState{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanTransitionSpecificRules(t *testing.T) {
	if CanTransition(TaskClaimed, TaskQueued) {
		t.Error("a claimed task must never return to queued (abandoned uses failed)")
	}
	for _, s := range AllTaskStates {
		if s.Terminal() {
			for _, to := range AllTaskStates {
				if CanTransition(s, to) {
					t.Errorf("terminal state %s may not move to %s", s, to)
				}
			}
		}
		if CanTransition(s, TaskSent) {
			t.Errorf("nothing may move back to sent (from %s)", s)
		}
	}
	if CanTransition("bogus", TaskDone) || CanTransition(TaskQueued, "bogus") {
		t.Error("unknown states must be refused")
	}
}
