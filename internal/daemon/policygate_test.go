package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// naiveHandler ignores the trust policy: it would deliver anything it is given.
type naiveHandler struct {
	calls     int
	decisions []Decision
}

func (h *naiveHandler) Handle(ctx context.Context, _ store.Peer, _ core.Envelope) error {
	h.calls++
	d, _ := DecisionFrom(ctx)
	h.decisions = append(h.decisions, d)
	return nil
}

func TestPolicyGateKeepsRejectedItemsFromTheHandler(t *testing.T) {
	cases := []struct {
		kind     core.Kind
		trust    core.TrustLevel
		inner    bool
		decision Decision
	}{
		{core.KindTaskCreate, core.TrustChatOnly, false, DecisionReject},
		{core.KindTaskCreate, core.TrustAskFirst, true, DecisionHold},
		{core.KindTaskCreate, core.TrustAutonomous, true, DecisionDeliver},
		{core.KindTaskCreate, core.TrustLevel(9), false, DecisionReject},
		{core.KindFileOffer, core.TrustChatOnly, true, DecisionHold},
		{core.KindFileOffer, core.TrustAskFirst, true, DecisionDeliver},
		{core.KindFileOffer, core.TrustLevel(9), false, DecisionReject},
	}
	for _, tc := range cases {
		inner, reject := &naiveHandler{}, &naiveHandler{}
		gate := PolicyGate{Inner: inner, OnReject: reject}
		peer := store.Peer{MachineID: "peer", Alias: "p", TrustIn: tc.trust}
		env := core.Envelope{ID: core.NewID(), Kind: tc.kind}
		if err := gate.Handle(context.Background(), peer, env); err != nil {
			t.Fatalf("%s/%d: %v", tc.kind, tc.trust, err)
		}
		if got := inner.calls == 1; got != tc.inner {
			t.Errorf("%s/%d: inner called %d times", tc.kind, tc.trust, inner.calls)
		}
		if tc.inner && inner.decisions[0] != tc.decision {
			t.Errorf("%s/%d: inner saw decision %s, want %s", tc.kind, tc.trust, inner.decisions[0], tc.decision)
		}
		if !tc.inner && (reject.calls != 1 || reject.decisions[0] != DecisionReject) {
			t.Errorf("%s/%d: OnReject calls %d", tc.kind, tc.trust, reject.calls)
		}
	}
	// Without OnReject a rejected item is dropped.
	inner := &naiveHandler{}
	if err := (PolicyGate{Inner: inner}).Handle(context.Background(),
		store.Peer{TrustIn: core.TrustChatOnly}, core.Envelope{Kind: core.KindTaskCreate}); err != nil {
		t.Fatal(err)
	}
	if inner.calls != 0 {
		t.Fatal("rejected task reached the handler")
	}
}

func TestHandleCreateRefusesGateDecisionMismatch(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "stranger", core.TrustChatOnly)
	id := core.NewID()
	env := d2Env(t, peer, core.KindTaskCreate, "codex@train", "", core.TaskCreateBody{TaskID: id, Instructions: "rm -rf"})
	err := e.tasks.HandleCreate(withDecision(ctx, DecisionDeliver), peer, env)
	if err == nil {
		t.Fatal("HandleCreate accepted a gate decision that contradicts the policy")
	}
	if _, err := e.st.GetTask(ctx, id); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("task stored despite the mismatch: %v", err)
	}
}

func TestRejectCreateRecordsAndReplies(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t)
	peer, _ := d2Peer(t, e.st, "stranger", core.TrustChatOnly)
	id := core.NewID()
	env := d2Env(t, peer, core.KindTaskCreate, "codex@train", "", core.TaskCreateBody{TaskID: id, Instructions: "x"})
	for i := 0; i < 2; i++ { // a redelivery is not rejected twice
		if err := e.tasks.RejectCreate(ctx, peer, env); err != nil {
			t.Fatal(err)
		}
	}
	if st := e.state(t, id).State; st != core.TaskRejected {
		t.Fatalf("state = %s", st)
	}
	ups := d2Updates(t, e.sender)
	if len(ups) != 1 || ups[0].TaskID != id || ups[0].State != core.TaskRejected {
		t.Fatalf("updates = %+v", ups)
	}
}

func TestHandleOfferRefusesGateDecisionMismatch(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	peer, _ := d2Peer(t, e.te.st, "stranger", core.TrustChatOnly)
	body := e.blobs.put(t, "a.txt", []byte("abc"))
	env := d2Env(t, peer, core.KindFileOffer, "", "", body)
	if err := e.files.HandleOffer(withDecision(ctx, DecisionDeliver), peer, env); err == nil {
		t.Fatal("HandleOffer accepted a gate decision that contradicts the policy")
	}
	if _, err := e.te.st.GetFile(ctx, body.FileID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("file recorded despite the mismatch: %v", err)
	}
}

func TestRejectOfferDeclines(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	peer, _ := d2Peer(t, e.te.st, "corrupt", core.TrustLevel(9))
	body := e.blobs.put(t, "a.txt", []byte("abc"))
	env := d2Env(t, peer, core.KindFileOffer, "", "", body)
	if err := e.files.RejectOffer(ctx, peer, env); err != nil {
		t.Fatal(err)
	}
	if r := e.record(t, body.FileID); r.State != store.FileDeclined || r.Reason != "not permitted" {
		t.Fatalf("record = %s (%s)", r.State, r.Reason)
	}
	if len(e.blobs.getCalls) != 0 {
		t.Fatal("rejected offer was downloaded")
	}
}

func TestWiredTaskCreateFromChatOnlyPeerIsRejected(t *testing.T) {
	ctx := context.Background()
	d := d2NewDaemon(t, t.TempDir(), &d2Relay{})
	defer d.Close()
	peer := newTestPeer(t, "stranger", core.TrustChatOnly)
	mustPut(t, d.store, peer.rec)
	h, ok := d.svc.Load().registry.Lookup(core.KindTaskCreate)
	if !ok {
		t.Fatal("no task.create handler")
	}
	id := core.NewID()
	env := d2Env(t, peer.rec, core.KindTaskCreate, "codex@x", "", core.TaskCreateBody{TaskID: id, Instructions: "x"})
	if err := h.Handle(ctx, peer.rec, env); err != nil {
		t.Fatal(err)
	}
	if tk, err := d.store.GetTask(ctx, id); err != nil || tk.State != core.TaskRejected {
		t.Fatalf("task = %+v, %v", tk, err)
	}
	if pending, _, _ := d.store.CountOutbox(ctx); pending != 1 {
		t.Fatalf("outbox pending = %d, want the task.update rejected", pending)
	}
}
