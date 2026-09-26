package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func testTask(id string, st core.TaskState) store.Task {
	return store.Task{
		ID: id, Direction: store.TaskInbound, Peer: "A", FromSession: "codex@train",
		Instructions: "run the tests", State: st, CreatedAt: t0, UpdatedAt: t0,
	}
}

func TestTaskRoundTrip(t *testing.T) {
	ctx := context.Background()
	ts := newTestDB(t)
	tk := testTask("t1", core.TaskQueued)
	tk.Notes = []store.TaskNote{{At: t0, Text: "started"}}
	tk.Files = []core.FileRef{{FileID: "f1", Name: "data.csv", Size: 10}}
	tk.ResultFiles = []core.FileRef{{FileID: "f2", Name: "out.txt", Size: 3}}
	tk.ExpiresAt = t0.Add(core.UnclaimedExpiry)
	if err := ts.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, err := ts.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Instructions != "run the tests" || got.State != core.TaskQueued || got.Direction != store.TaskInbound ||
		len(got.Notes) != 1 || got.Notes[0].Text != "started" || !got.Notes[0].At.Equal(t0) ||
		len(got.Files) != 1 || got.Files[0].Name != "data.csv" || len(got.ResultFiles) != 1 ||
		!got.ExpiresAt.Equal(t0.Add(core.UnclaimedExpiry)) || !got.CreatedAt.Equal(t0) {
		t.Fatalf("round trip = %+v", got)
	}
	if _, err := ts.GetTask(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing task err = %v", err)
	}
}

func TestTaskTransitionCAS(t *testing.T) {
	ctx := context.Background()
	ts := newTestDB(t)
	if err := ts.PutTask(ctx, testTask("t1", core.TaskAwaitingApproval)); err != nil {
		t.Fatal(err)
	}
	claim := func(tk *store.Task) error {
		tk.State = core.TaskClaimed
		tk.ClaimedBy = "claude@proj"
		return nil
	}
	// Wrong state: cannot claim a task still awaiting approval.
	_, err := ts.Transition(ctx, "t1", []core.TaskState{core.TaskQueued}, claim)
	if !errors.Is(err, core.ErrBadTransition) {
		t.Fatalf("claim of awaiting task err = %v, want ErrBadTransition", err)
	}
	got, _ := ts.GetTask(ctx, "t1")
	if got.State != core.TaskAwaitingApproval || got.ClaimedBy != "" {
		t.Fatalf("failed transition changed the row: %+v", got)
	}
	// Approve, then claim.
	if _, err := ts.Transition(ctx, "t1", []core.TaskState{core.TaskAwaitingApproval}, func(tk *store.Task) error {
		tk.State = core.TaskQueued
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out, err := ts.Transition(ctx, "t1", []core.TaskState{core.TaskQueued}, claim)
	if err != nil || out.State != core.TaskClaimed || out.ClaimedBy != "claude@proj" {
		t.Fatalf("claim = %+v, %v", out, err)
	}
	// A mutate error aborts without writing.
	boom := errors.New("boom")
	_, err = ts.Transition(ctx, "t1", []core.TaskState{core.TaskClaimed}, func(tk *store.Task) error {
		tk.State = core.TaskDone
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("mutate error = %v", err)
	}
	got, _ = ts.GetTask(ctx, "t1")
	if got.State != core.TaskClaimed {
		t.Fatalf("aborted mutate was written: %s", got.State)
	}
	// The mutate func cannot rename the row.
	out, err = ts.Transition(ctx, "t1", []core.TaskState{core.TaskClaimed}, func(tk *store.Task) error {
		tk.ID = "other"
		tk.State = core.TaskRunning
		return nil
	})
	if err != nil || out.ID != "t1" {
		t.Fatalf("rename attempt = %+v, %v", out, err)
	}
	if _, err := ts.GetTask(ctx, "other"); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("mutate created a second row")
	}
	if _, err := ts.Transition(ctx, "missing", []core.TaskState{core.TaskQueued}, claim); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing task transition err = %v", err)
	}
}

func TestTaskConcurrentClaimExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	ts := newTestDB(t)
	for round := 0; round < 20; round++ {
		id := "t" + string(rune('a'+round))
		if err := ts.PutTask(ctx, testTask(id, core.TaskQueued)); err != nil {
			t.Fatal(err)
		}
		const racers = 8
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			winners []string
			losers  int
		)
		for i := 0; i < racers; i++ {
			wg.Add(1)
			go func(who string) {
				defer wg.Done()
				_, err := ts.Transition(ctx, id, []core.TaskState{core.TaskQueued}, func(tk *store.Task) error {
					tk.State = core.TaskClaimed
					tk.ClaimedBy = who
					return nil
				})
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					winners = append(winners, who)
				case errors.Is(err, core.ErrBadTransition):
					losers++
				default:
					t.Errorf("unexpected error: %v", err)
				}
			}("s" + string(rune('0'+i)))
		}
		wg.Wait()
		if len(winners) != 1 || losers != racers-1 {
			t.Fatalf("round %d: winners=%v losers=%d", round, winners, losers)
		}
		got, _ := ts.GetTask(ctx, id)
		if got.ClaimedBy != winners[0] {
			t.Fatalf("stored claimer %q, winner %q", got.ClaimedBy, winners[0])
		}
	}
}

func TestListTasksFilters(t *testing.T) {
	ctx := context.Background()
	ts := newTestDB(t)
	mk := func(id string, dir store.TaskDirection, peer core.MachineID, st core.TaskState, claimedBy string, created time.Time, expires time.Time) {
		tk := testTask(id, st)
		tk.Direction, tk.Peer, tk.ClaimedBy, tk.CreatedAt, tk.ExpiresAt = dir, peer, claimedBy, created, expires
		if err := ts.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	mk("in-await", store.TaskInbound, "A", core.TaskAwaitingApproval, "", t0, t0.Add(core.ApprovalExpiry))
	mk("in-claimed", store.TaskInbound, "A", core.TaskClaimed, "claude@proj", t0.Add(time.Second), time.Time{})
	mk("in-queued-b", store.TaskInbound, "B", core.TaskQueued, "", t0.Add(2*time.Second), t0.Add(time.Hour))
	mk("out-sent", store.TaskOutbound, "B", core.TaskSent, "", t0.Add(3*time.Second), time.Time{})

	ids := func(f store.TaskFilter) []string {
		t.Helper()
		list, err := ts.ListTasks(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(list))
		for i, tk := range list {
			out[i] = tk.ID
		}
		return out
	}
	cases := []struct {
		name string
		f    store.TaskFilter
		want []string
	}{
		{"all", store.TaskFilter{}, []string{"in-await", "in-claimed", "in-queued-b", "out-sent"}},
		{"outbound", store.TaskFilter{Direction: store.TaskOutbound}, []string{"out-sent"}},
		{"states", store.TaskFilter{States: []core.TaskState{core.TaskQueued, core.TaskAwaitingApproval}}, []string{"in-await", "in-queued-b"}},
		{"claimedBy", store.TaskFilter{ClaimedBy: "claude@proj"}, []string{"in-claimed"}},
		{"peer", store.TaskFilter{Peer: "B"}, []string{"in-queued-b", "out-sent"}},
		{"expired at 1h", store.TaskFilter{ExpiredBefore: t0.Add(time.Hour)}, []string{"in-queued-b"}},
		{"expired at 24h", store.TaskFilter{ExpiredBefore: t0.Add(core.ApprovalExpiry)}, []string{"in-await", "in-queued-b"}},
		{"combined", store.TaskFilter{Direction: store.TaskInbound, Peer: "A", States: []core.TaskState{core.TaskClaimed}}, []string{"in-claimed"}},
	}
	for _, c := range cases {
		if got := ids(c.f); !equalStrings(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTaskLinkIDRoundTripAndFilter(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	for _, tk := range []store.Task{
		{ID: "T1", Direction: store.TaskInbound, Peer: "m1", LinkID: "L1", State: core.TaskQueued, CreatedAt: t0},
		{ID: "T2", Direction: store.TaskInbound, Peer: "m1", LinkID: "L2", State: core.TaskQueued, CreatedAt: t0},
	} {
		if err := db.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	got, err := db.GetTask(ctx, "T1")
	if err != nil || got.LinkID != "L1" {
		t.Fatalf("GetTask = %+v, %v", got, err)
	}
	list, err := db.ListTasks(ctx, store.TaskFilter{LinkID: "L2"})
	if err != nil || len(list) != 1 || list[0].ID != "T2" {
		t.Fatalf("ListTasks(LinkID) = %+v, %v", list, err)
	}
}
