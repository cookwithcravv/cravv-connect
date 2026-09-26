package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/store/sqlite"
)

// An in-process network of v2 test machines. Envelopes sent through the
// outbox (SendEnvelope) wait in a queue until pump delivers them in order;
// direct sends (SendDirect) are delivered at once unless the network holds
// them. Handlers get the receiver's own peer record for the sender, exactly
// like Inbound gives them.

type v2Frame struct {
	from, to core.MachineID
	env      core.Envelope
}

type v2Net struct {
	t     *testing.T
	clock *core.FakeClock

	mu         sync.Mutex
	nodes      map[core.MachineID]*v2Node
	queue      []v2Frame
	holdDirect bool
	log        []v2Frame // every frame sent, in order
}

func newV2Net(t *testing.T) *v2Net {
	return &v2Net{t: t, clock: core.NewFakeClock(d2Epoch), nodes: map[core.MachineID]*v2Node{}}
}

// v2Node is one machine: a real SQLite store, sessions and a handler registry.
type v2Node struct {
	net      *v2Net
	name     string
	ident    *keys.Identity
	id       core.MachineID
	st       *sqlite.DB
	shared   *SessionService
	peers    *PeerService
	registry *HandlerRegistry
	sender   *v2Sender
	discover *Discovery
}

func (n *v2Net) node(name string) *v2Node {
	n.t.Helper()
	id, err := keys.GenerateIdentity()
	if err != nil {
		n.t.Fatal(err)
	}
	st := d2Store(n.t)
	v := &v2Node{net: n, name: name, ident: id, id: id.MachineID(), st: st, registry: NewHandlerRegistry()}
	v.sender = &v2Sender{node: v}
	v.shared = NewSessionService(st, n.clock)
	v.peers = NewPeerService(st, &mailboxSlot{}, v.sender, nil, n.clock)
	v.discover = NewDiscovery(v.shared, v.peers, v.sender, n.clock, nil)
	v.registry.Register(core.KindSessionsList, HandlerFunc(v.discover.HandleList))
	v.registry.Register(core.KindSessionsListed, HandlerFunc(v.discover.HandleListed))
	n.mu.Lock()
	n.nodes[v.id] = v
	n.mu.Unlock()
	return v
}

// pair makes a and b known to each other under their names.
func (n *v2Net) pair(a, b *v2Node) {
	n.t.Helper()
	for _, x := range [][2]*v2Node{{a, b}, {b, a}} {
		if err := x[0].st.PutPeer(context.Background(), store.Peer{
			MachineID: x[1].id, IK: x[1].ident.Public(), Alias: x[1].name, PairedAt: d2Epoch,
		}); err != nil {
			n.t.Fatal(err)
		}
	}
}

// peerRec is how node v knows the machine other.
func (v *v2Node) peerRec(other *v2Node) store.Peer {
	v.net.t.Helper()
	p, err := v.st.GetPeer(context.Background(), other.id)
	if err != nil {
		v.net.t.Fatal(err)
	}
	return p
}

// deliver runs the receiver's handler for one frame.
func (n *v2Net) deliver(f v2Frame) {
	n.mu.Lock()
	to, from := n.nodes[f.to], n.nodes[f.from]
	n.mu.Unlock()
	if to == nil || from == nil {
		return
	}
	peer, err := to.st.GetPeer(context.Background(), f.from)
	if err != nil {
		return // unpaired: dropped like Inbound drops an unknown sender
	}
	if peer.Paused {
		return
	}
	h, ok := to.registry.Lookup(f.env.Kind)
	if !ok {
		return
	}
	if err := h.Handle(context.Background(), peer, f.env); err != nil {
		n.t.Logf("%s: %s handler: %v", to.name, f.env.Kind, err)
	}
}

// pump delivers queued frames in order until none are left.
func (n *v2Net) pump() {
	for {
		n.mu.Lock()
		if len(n.queue) == 0 {
			n.mu.Unlock()
			return
		}
		f := n.queue[0]
		n.queue = n.queue[1:]
		n.mu.Unlock()
		n.deliver(f)
	}
}

// dropQueued discards queued frames of kind (a lost or overtaken message).
func (n *v2Net) dropQueued(kind core.Kind) {
	n.mu.Lock()
	defer n.mu.Unlock()
	kept := n.queue[:0]
	for _, f := range n.queue {
		if f.env.Kind != kind {
			kept = append(kept, f)
		}
	}
	n.queue = kept
}

// sent returns every frame of kind sent so far, oldest first.
func (n *v2Net) sent(kind core.Kind) []v2Frame {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []v2Frame
	for _, f := range n.log {
		if f.env.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

// v2Sender is a node's outbox and direct sender on the test network.
type v2Sender struct{ node *v2Node }

func (s *v2Sender) envelope(to core.MachineID, kind core.Kind, body any) (v2Frame, error) {
	env, err := core.NewEnvelope(s.node.net.clock, s.node.id, to, kind, body)
	if err != nil {
		return v2Frame{}, err
	}
	return v2Frame{from: s.node.id, to: to, env: env}, nil
}

func (s *v2Sender) SendEnvelope(_ context.Context, to core.MachineID, kind core.Kind, _, _ string, body any) (string, error) {
	f, err := s.envelope(to, kind, body)
	if err != nil {
		return "", err
	}
	n := s.node.net
	n.mu.Lock()
	n.queue = append(n.queue, f)
	n.log = append(n.log, f)
	n.mu.Unlock()
	return f.env.ID, nil
}

func (s *v2Sender) SendDirect(_ context.Context, peer store.Peer, kind core.Kind, body any) error {
	f, err := s.envelope(peer.MachineID, kind, body)
	if err != nil {
		return err
	}
	n := s.node.net
	n.mu.Lock()
	n.log = append(n.log, f)
	hold := n.holdDirect
	if hold {
		n.queue = append(n.queue, f)
	}
	n.mu.Unlock()
	if !hold {
		n.deliver(f)
	}
	return nil
}

func (s *v2Sender) Hold(context.Context, core.MachineID) error    { return nil }
func (s *v2Sender) Release(context.Context, core.MachineID) error { return nil }
func (s *v2Sender) Forget(context.Context, core.MachineID) error  { return nil }

// v2Body decodes a frame body.
func v2Body[T any](t *testing.T, f v2Frame) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(f.env.Body, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
