package daemon

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/store/sqlite"
	"github.com/cravv/cravv-connect/internal/transport"
)

// simRelay routes frames between simBoxes and enforces each owner's deny list,
// answering not_allowed like the real relay.
type simRelay struct {
	mu    sync.Mutex
	boxes map[core.MachineID]*simBox
}

type simBox struct {
	transport.Mailbox // unused methods panic
	relay             *simRelay
	owner             ed25519.PublicKey
	denied            map[string]bool
	queue             []transport.Delivery
	seq               uint64
}

func (r *simRelay) box(owner *keys.Identity) *simBox {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := &simBox{relay: r, owner: owner.Public(), denied: map[string]bool{}}
	r.boxes[owner.MachineID()] = b
	return b
}

func (b *simBox) Send(_ context.Context, to core.MachineID, id string, frame []byte) (transport.SendStatus, error) {
	b.relay.mu.Lock()
	defer b.relay.mu.Unlock()
	target, ok := b.relay.boxes[to]
	if !ok {
		return transport.SendUnknownMailbox, nil
	}
	if target.denied[string(b.owner)] {
		return transport.SendNotAllowed, nil
	}
	target.seq++
	target.queue = append(target.queue, transport.Delivery{Seq: target.seq, From: b.owner, ID: id, Frame: frame})
	return transport.SendQueued, nil
}

func (b *simBox) Allow(_ context.Context, ik ed25519.PublicKey) error {
	b.relay.mu.Lock()
	defer b.relay.mu.Unlock()
	delete(b.denied, string(ik))
	return nil
}

func (b *simBox) Deny(_ context.Context, ik ed25519.PublicKey) error {
	b.relay.mu.Lock()
	defer b.relay.mu.Unlock()
	b.denied[string(ik)] = true
	return nil
}

func (b *simBox) take() []transport.Delivery {
	b.relay.mu.Lock()
	defer b.relay.mu.Unlock()
	q := b.queue
	b.queue = nil
	return q
}

// simNode is one machine wired from the real services over a sqlite store.
type simNode struct {
	id    *keys.Identity
	db    *sqlite.DB
	box   *simBox
	out   *Outbound
	peers *PeerService
	in    *Inbound
	pk    keys.SignedPrekey

	mu    sync.Mutex
	chats []string
}

func newSimNode(t *testing.T, relay *simRelay, clock core.Clock) *simNode {
	t.Helper()
	id, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	n := &simNode{id: id, db: d2Store(t)}
	n.box = relay.box(id)
	slot := &mailboxSlot{}
	slot.set(n.box)
	n.out = NewOutbound(id, n.db, n.db, slot, clock, nil, nil)
	n.peers = NewPeerService(n.db, slot, n.out, &recordingAudit{}, clock)
	pm := NewPrekeyManager(n.db, n.db, id, n.out, clock)
	if n.pk, err = pm.EnsureCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg := NewHandlerRegistry()
	RegisterControlHandlers(reg, n.db, n.peers, n.out)
	reg.Register(core.KindChat, HandlerFunc(func(_ context.Context, _ store.Peer, env core.Envelope) error {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.chats = append(n.chats, env.ID)
		return nil
	}))
	n.in = NewInbound(id, n.db, n.db, pm, reg, n.out, clock, nil)
	return n
}

func (n *simNode) received() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.chats...)
}

func simPair(t *testing.T, a, b *simNode, aliasOfB, aliasOfA string) {
	t.Helper()
	mustPut(t, a.db, store.Peer{MachineID: b.id.MachineID(), IK: b.id.Public(), Alias: aliasOfB,
		TrustIn: core.TrustAskFirst, Prekey: b.pk.Wire(), RelayURL: "https://relay.test", PairedAt: testEpoch})
	mustPut(t, b.db, store.Peer{MachineID: a.id.MachineID(), IK: a.id.Public(), Alias: aliasOfA,
		TrustIn: core.TrustAskFirst, Prekey: a.pk.Wire(), RelayURL: "https://relay.test", PairedAt: testEpoch})
}

// settle runs send passes and deliveries until nothing moves, advancing the clock past
// any backoff each round.
func settle(t *testing.T, clock *core.FakeClock, nodes ...*simNode) {
	t.Helper()
	ctx := context.Background()
	for round := 0; round < 30; round++ {
		clock.Advance(core.BackoffMax)
		for _, n := range nodes {
			if err := n.out.SendDue(ctx); err != nil {
				t.Fatal(err)
			}
		}
		for _, n := range nodes {
			for _, d := range n.box.take() {
				if err := n.in.process(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
			n.in.flushReceipts(ctx)
		}
	}
}

func sendChat(t *testing.T, from *simNode, to *simNode, text string) string {
	t.Helper()
	id, err := from.out.SendEnvelope(context.Background(), to.id.MachineID(), core.KindChat, "", "", core.ChatBody{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMutualPauseConverges(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(testEpoch)
	relay := &simRelay{boxes: map[core.MachineID]*simBox{}}
	a := newSimNode(t, relay, clock)
	b := newSimNode(t, relay, clock)
	simPair(t, a, b, "bee", "ay")

	if err := a.peers.Pause(ctx, "bee"); err != nil {
		t.Fatal(err)
	}
	settle(t, clock, a, b)
	fromB := sendChat(t, b, a, "while A paused B") // B knows it is paused: held
	if err := b.peers.Pause(ctx, "ay"); err != nil {
		t.Fatal(err)
	}
	settle(t, clock, a, b)
	if err := a.peers.Resume(ctx, "bee"); err != nil {
		t.Fatal(err)
	}
	fromA := sendChat(t, a, b, "after A resumed") // B still pauses A
	settle(t, clock, a, b)
	if err := b.peers.Resume(ctx, "ay"); err != nil {
		t.Fatal(err)
	}
	settle(t, clock, a, b)

	for _, c := range []struct {
		n    *simNode
		peer core.MachineID
		name string
	}{{a, b.id.MachineID(), "A's record of B"}, {b, a.id.MachineID(), "B's record of A"}} {
		p := mustGetPeer(t, c.n.db, c.peer)
		if p.Paused || p.PausedByPeer {
			t.Errorf("%s: paused %v paused_by_peer %v, want both false", c.name, p.Paused, p.PausedByPeer)
		}
		if _, held, _ := c.n.db.CountOutbox(ctx); held != 0 {
			t.Errorf("%s: %d items still held", c.name, held)
		}
	}
	if got := a.received(); len(got) != 1 || got[0] != fromB {
		t.Errorf("A received %v, want [%s]", got, fromB)
	}
	if got := b.received(); len(got) != 1 || got[0] != fromA {
		t.Errorf("B received %v, want [%s]", got, fromA)
	}
}
