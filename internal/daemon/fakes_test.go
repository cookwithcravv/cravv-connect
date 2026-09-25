package daemon

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

var testEpoch = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// callLog records calls across fakes so tests can assert ordering.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(format string, args ...any) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf(format, args...))
}

func (l *callLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type sentFrame struct {
	To    core.MachineID
	ID    string
	Frame []byte
}

// fakeMailbox is a scripted transport.Mailbox.
type fakeMailbox struct {
	mu         sync.Mutex
	log        *callLog
	statuses   []transport.SendStatus // returned in order; SendQueued once exhausted
	sendErr    error
	sent       []sentFrame
	acks       []uint64
	allowed    map[string]bool
	denied     map[string]bool
	invite     string
	nameplate  string
	token      string
	deliveries chan transport.Delivery
	done       chan struct{}
}

func newFakeMailbox(log *callLog) *fakeMailbox {
	return &fakeMailbox{
		log:        log,
		allowed:    map[string]bool{},
		denied:     map[string]bool{},
		invite:     "invite-1",
		nameplate:  "7K3F",
		token:      "creator-token",
		deliveries: make(chan transport.Delivery, 64),
		done:       make(chan struct{}),
	}
}

func (f *fakeMailbox) Send(_ context.Context, to core.MachineID, id string, frame []byte) (transport.SendStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log.add("send %s", id)
	if f.sendErr != nil {
		return "", f.sendErr
	}
	f.sent = append(f.sent, sentFrame{To: to, ID: id, Frame: append([]byte(nil), frame...)})
	if len(f.statuses) == 0 {
		return transport.SendQueued, nil
	}
	st := f.statuses[0]
	f.statuses = f.statuses[1:]
	return st, nil
}

func (f *fakeMailbox) Deliveries() <-chan transport.Delivery { return f.deliveries }

func (f *fakeMailbox) Ack(_ context.Context, seq uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acks = append(f.acks, seq)
	return nil
}

func (f *fakeMailbox) Allow(_ context.Context, ik ed25519.PublicKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log.add("allow %s", keys.MachineIDOf(ik).Short())
	f.allowed[string(ik)] = true
	delete(f.denied, string(ik))
	return nil
}

func (f *fakeMailbox) Deny(_ context.Context, ik ed25519.PublicKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log.add("deny %s", keys.MachineIDOf(ik).Short())
	f.denied[string(ik)] = true
	delete(f.allowed, string(ik))
	return nil
}

func (f *fakeMailbox) RequestInvite(context.Context) (string, error) { return f.invite, nil }

func (f *fakeMailbox) CreateRoom(context.Context) (string, string, error) {
	return f.nameplate, f.token, nil
}

func (f *fakeMailbox) Done() <-chan struct{} { return f.done }
func (f *fakeMailbox) Err() error            { return nil }
func (f *fakeMailbox) Close() error          { return nil }

func (f *fakeMailbox) sentFrames() []sentFrame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentFrame(nil), f.sent...)
}

func (f *fakeMailbox) ackedSeqs() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint64(nil), f.acks...)
}

func (f *fakeMailbox) isAllowed(ik ed25519.PublicKey) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.allowed[string(ik)]
}

func (f *fakeMailbox) isDenied(ik ed25519.PublicKey) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.denied[string(ik)]
}

// mailboxSlot is a MailboxProvider whose mailbox can be swapped (nil = offline).
type mailboxSlot struct {
	mu sync.Mutex
	mb transport.Mailbox
}

func (s *mailboxSlot) Mailbox() (transport.Mailbox, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mb, s.mb != nil
}

func (s *mailboxSlot) set(mb transport.Mailbox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mb = mb
}

// recordingAudit keeps every audit event.
type recordingAudit struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *recordingAudit) Record(e audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *recordingAudit) types() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.events))
	for i, e := range a.events {
		out[i] = e.Type
	}
	return out
}

func (a *recordingAudit) last() audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 {
		return audit.Event{}
	}
	return a.events[len(a.events)-1]
}

type sentEnvelope struct {
	To          core.MachineID
	Kind        core.Kind
	FromSession string
	ToSession   string
	Body        json.RawMessage
}

// recordingSender is an EnvelopeSender and OutboxControl that records instead of sending.
type recordingSender struct {
	mu       sync.Mutex
	log      *callLog
	err      error // returned by SendEnvelope when set
	envs     []sentEnvelope
	direct   []sentEnvelope
	held     []core.MachineID
	released []core.MachineID
	forgot   []core.MachineID
}

func (r *recordingSender) SendEnvelope(_ context.Context, to core.MachineID, kind core.Kind, fromSession, toSession string, body any) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return "", r.err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	r.log.add("envelope %s %s", kind, to.Short())
	r.envs = append(r.envs, sentEnvelope{To: to, Kind: kind, FromSession: fromSession, ToSession: toSession, Body: b})
	return core.NewID(), nil
}

func (r *recordingSender) SendDirect(_ context.Context, peer store.Peer, kind core.Kind, body any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	r.log.add("direct %s %s", kind, peer.MachineID.Short())
	r.direct = append(r.direct, sentEnvelope{To: peer.MachineID, Kind: kind, Body: b})
	return nil
}

func (r *recordingSender) Hold(_ context.Context, peer core.MachineID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.add("hold %s", peer.Short())
	r.held = append(r.held, peer)
	return nil
}

func (r *recordingSender) Release(_ context.Context, peer core.MachineID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.add("release %s", peer.Short())
	r.released = append(r.released, peer)
	return nil
}

func (r *recordingSender) Forget(_ context.Context, peer core.MachineID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log.add("forget %s", peer.Short())
	r.forgot = append(r.forgot, peer)
	return nil
}

func (r *recordingSender) envelopes() []sentEnvelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sentEnvelope(nil), r.envs...)
}

func (r *recordingSender) ofKind(k core.Kind) []sentEnvelope {
	var out []sentEnvelope
	for _, e := range r.envelopes() {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// testPeer is a remote machine: its identity, its current prekey, and the record we store for it.
type testPeer struct {
	id     *keys.Identity
	prekey *keys.Prekey
	rec    store.Peer
}

func newTestPeer(t *testing.T, alias string, trust core.TrustLevel) testPeer {
	t.Helper()
	id, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pk, err := keys.GeneratePrekey(testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	return testPeer{id: id, prekey: pk, rec: store.Peer{
		MachineID: id.MachineID(),
		IK:        id.Public(),
		Alias:     alias,
		TrustIn:   trust,
		Prekey:    pk.Signed(id).Wire(),
		RelayURL:  "https://relay.test",
		PairedAt:  testEpoch,
	}}
}

// resolver is a sealing.PrekeyResolver over a fixed set of prekeys.
type resolverMap map[string]*keys.Prekey

func (r resolverMap) PrivatePrekey(id string) (*ecdh.PrivateKey, bool) {
	p, ok := r[id]
	if !ok {
		return nil, false
	}
	return p.Priv, true
}

func mustPut(t *testing.T, ps store.PeerStore, p store.Peer) {
	t.Helper()
	if err := ps.PutPeer(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func mustGetPeer(t *testing.T, ps store.PeerStore, id core.MachineID) store.Peer {
	t.Helper()
	p, err := ps.GetPeer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

var errBoom = errors.New("boom")
