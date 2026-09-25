package daemon

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/bindcode"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/pake"
	"github.com/cravv/cravv-connect/internal/transport"
)

// memRooms is an in-memory transport.Rooms with relay-v1 room rules: the creator needs the
// token, one joiner only, and closing either side burns the room.
type memRooms struct {
	mu     sync.Mutex
	rooms  map[string]*memRoomPair
	tamper func(fromCreator bool, index int, data []byte) []byte
}

type memRoomPair struct {
	token        string
	joined       chan struct{}
	toCreator    chan []byte
	toJoiner     chan []byte
	closed       chan struct{}
	closeOnce    sync.Once
	joinerOpened bool
	sentByA      int
	sentByB      int
}

func newMemRooms() *memRooms { return &memRooms{rooms: map[string]*memRoomPair{}} }

// create is what the relay does on room_create.
func (r *memRooms) create(nameplate, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rooms[nameplate] = &memRoomPair{token: token, joined: make(chan struct{}),
		toCreator: make(chan []byte, 8), toJoiner: make(chan []byte, 8), closed: make(chan struct{})}
}

func (r *memRooms) Open(_ context.Context, nameplate, token string) (transport.Room, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rp, ok := r.rooms[nameplate]
	if !ok {
		return nil, transport.ErrRoomGone
	}
	select {
	case <-rp.closed:
		return nil, transport.ErrRoomGone
	default:
	}
	if token != "" {
		if token != rp.token {
			return nil, transport.ErrRoomGone
		}
		return &memRoomEnd{rooms: r, pair: rp, creator: true}, nil
	}
	if rp.joinerOpened {
		return nil, transport.ErrRoomGone
	}
	rp.joinerOpened = true
	close(rp.joined)
	return &memRoomEnd{rooms: r, pair: rp, creator: false}, nil
}

type memRoomEnd struct {
	rooms   *memRooms
	pair    *memRoomPair
	creator bool
}

func (e *memRoomEnd) WaitPeer(ctx context.Context) error {
	if !e.creator {
		return nil
	}
	select {
	case <-e.pair.joined:
		return nil
	case <-e.pair.closed:
		return io.EOF
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *memRoomEnd) Send(ctx context.Context, data []byte) error {
	e.rooms.mu.Lock()
	out := e.pair.toJoiner
	idx := e.pair.sentByA
	if e.creator {
		e.pair.sentByA++
	} else {
		out = e.pair.toCreator
		idx = e.pair.sentByB
		e.pair.sentByB++
	}
	tamper := e.rooms.tamper
	e.rooms.mu.Unlock()
	data = append([]byte(nil), data...)
	if tamper != nil {
		data = tamper(e.creator, idx, data)
	}
	select {
	case out <- data:
		return nil
	case <-e.pair.closed:
		return io.EOF
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *memRoomEnd) Recv(ctx context.Context) ([]byte, error) {
	in := e.pair.toCreator
	if !e.creator {
		in = e.pair.toJoiner
	}
	select {
	case b := <-in:
		return b, nil
	case <-e.pair.closed:
		select { // drain anything sent before the close
		case b := <-in:
			return b, nil
		default:
			return nil, io.EOF
		}
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *memRoomEnd) Close() error {
	e.pair.closeOnce.Do(func() { close(e.pair.closed) })
	return nil
}

// roomMailbox is a fakeMailbox whose CreateRoom registers the room with memRooms.
type roomMailbox struct {
	*fakeMailbox
	rooms *memRooms
}

func (m *roomMailbox) CreateRoom(ctx context.Context) (string, string, error) {
	np, tok, err := m.fakeMailbox.CreateRoom(ctx)
	if err == nil {
		m.rooms.create(np, tok)
	}
	return np, tok, err
}

type fakeRegistrar struct {
	registered bool
	invites    []string
}

func (r *fakeRegistrar) Registered() bool { return r.registered }

func (r *fakeRegistrar) EnsureRegistered(_ context.Context, invite string) error {
	r.invites = append(r.invites, invite)
	r.registered = true
	return nil
}

type pairSide struct {
	svc   *PairingService
	id    *keys.Identity
	peers *memPeers
	mb    *fakeMailbox
	slot  *mailboxSlot
	reg   *fakeRegistrar
	audit *recordingAudit
	clock *core.FakeClock
}

func newPairSide(t *testing.T, rooms *memRooms, name string, online bool) *pairSide {
	t.Helper()
	id, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	s := &pairSide{id: id, peers: newMemPeers(), slot: &mailboxSlot{}, reg: &fakeRegistrar{registered: online},
		audit: &recordingAudit{}, clock: core.NewFakeClock(testEpoch)}
	s.mb = newFakeMailbox(nil)
	if online {
		s.slot.set(&roomMailbox{fakeMailbox: s.mb, rooms: rooms})
	}
	prekeys := NewPrekeyManager(newMemPrekeys(), s.peers, id, &recordingSender{}, s.clock)
	s.svc = NewPairingService(id, rooms, s.slot, pake.SPAKE2{}, s.peers, prekeys, s.reg,
		PairingConfig{DeviceName: name, RelayURL: "https://relay.test"}, s.clock, s.audit)
	return s
}

func TestPairingFullExchange(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "Prith's MacBook", true)
	b := newPairSide(t, rooms, "GPU Box", false) // joiner has no mailbox yet

	pendingA, code, err := a.svc.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindcode.Parse(code); err != nil {
		t.Fatalf("code %q does not parse: %v", code, err)
	}
	propB, err := b.svc.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	propA, err := a.svc.Await(ctx, pendingA)
	if err != nil {
		t.Fatal(err)
	}
	if propA.MachineID != b.id.MachineID() || propB.MachineID != a.id.MachineID() {
		t.Fatal("proposals carry the wrong machine IDs")
	}
	if propA.SuggestedName != "gpu-box" || propB.SuggestedName != "prith-s-macbook" {
		t.Fatalf("suggested names %q / %q", propA.SuggestedName, propB.SuggestedName)
	}

	aliasA, err := a.svc.Finalize(ctx, pendingA, "", core.TrustAskFirst)
	if err != nil {
		t.Fatal(err)
	}
	aliasB, err := b.svc.Finalize(ctx, propB.PendingID, "My Mac", core.TrustAutonomous)
	if err != nil {
		t.Fatal(err)
	}
	if aliasA != "gpu-box" || aliasB != "my-mac" {
		t.Fatalf("aliases %q / %q", aliasA, aliasB)
	}

	if got := b.reg.invites; len(got) != 1 || got[0] != a.mb.invite {
		t.Fatalf("joiner registered with %v, want creator's invite", got)
	}
	if len(a.reg.invites) != 0 {
		t.Fatal("creator re-registered")
	}

	pa := mustGetPeer(t, a.peers, b.id.MachineID())
	if pa.TrustIn != core.TrustAskFirst || pa.RelayURL != "https://relay.test" || !pa.PairedAt.Equal(testEpoch) {
		t.Fatalf("stored peer on A = %+v", pa)
	}
	if err := keys.SignedPrekeyFromWire(pa.Prekey).Verify(b.id.Public()); err != nil {
		t.Fatalf("A stored an unverifiable prekey for B: %v", err)
	}
	pb := mustGetPeer(t, b.peers, a.id.MachineID())
	if pb.TrustIn != core.TrustAutonomous || pb.Alias != "my-mac" {
		t.Fatalf("stored peer on B = %+v", pb)
	}
	if !a.mb.isAllowed(b.id.Public()) {
		t.Fatal("creator did not allow the new peer on its mailbox")
	}
	for _, s := range []*pairSide{a, b} {
		if ev := s.audit.last(); ev.Type != audit.EvPair {
			t.Fatalf("audit = %v", s.audit.types())
		}
	}
	if _, err := a.svc.Finalize(ctx, pendingA, "again", core.TrustAskFirst); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("second finalize err = %v", err)
	}
	if _, err := newPairSide(t, rooms, "late", true).svc.Join(ctx, code); err == nil {
		t.Fatal("a used code joined again")
	}
}

func wrongSecret(c bindcode.Code) bindcode.Code {
	w := c
	w.Secret = "ZZZZZZZZ"
	if w.Secret == c.Secret {
		w.Secret = "YYYYYYYY"
	}
	return w
}

func TestPairingWrongCodeFailsBothAndBurnsRoom(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "a", true)
	b := newPairSide(t, rooms, "b", false)
	pendingA, code, err := a.svc.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := bindcode.Parse(code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.svc.Join(ctx, wrongSecret(c).String()); !errors.Is(err, ErrPairingFailed) {
		t.Fatalf("joiner err = %v, want ErrPairingFailed", err)
	}
	if _, err := a.svc.Await(ctx, pendingA); !errors.Is(err, ErrPairingFailed) {
		t.Fatalf("creator err = %v, want ErrPairingFailed", err)
	}
	if _, err := b.svc.Join(ctx, code); err == nil {
		t.Fatal("room still usable after a failed attempt")
	}
	if n := len(a.peers.m) + len(b.peers.m); n != 0 {
		t.Fatalf("%d peers stored after a failed pairing", n)
	}
}

func TestPairingTamperedPayloadFailsBoth(t *testing.T) {
	for _, fromCreator := range []bool{true, false} {
		ctx := context.Background()
		rooms := newMemRooms()
		rooms.tamper = func(creator bool, index int, data []byte) []byte {
			if creator == fromCreator && index == 2 { // 0 pake, 1 confirm, 2 payload
				data[len(data)-1] ^= 0x01
			}
			return data
		}
		a := newPairSide(t, rooms, "a", true)
		b := newPairSide(t, rooms, "b", false)
		pendingA, code, err := a.svc.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.svc.Join(ctx, code); !errors.Is(err, ErrPairingFailed) {
			t.Fatalf("tamper from creator=%v: joiner err = %v", fromCreator, err)
		}
		if _, err := a.svc.Await(ctx, pendingA); !errors.Is(err, ErrPairingFailed) {
			t.Fatalf("tamper from creator=%v: creator err = %v", fromCreator, err)
		}
	}
}

func TestPairingExpiredPending(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "a", true)
	b := newPairSide(t, rooms, "b", false)
	pendingA, code, err := a.svc.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prop, err := b.svc.Join(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Await(ctx, pendingA); err != nil {
		t.Fatal(err)
	}
	a.clock.Advance(core.RoomTTL + time.Second)
	b.clock.Advance(core.RoomTTL + time.Second)
	if _, err := a.svc.Finalize(ctx, pendingA, "b", core.TrustAskFirst); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("creator finalize err = %v, want ErrPairingExpired", err)
	}
	if _, err := b.svc.Finalize(ctx, prop.PendingID, "a", core.TrustAskFirst); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("joiner finalize err = %v, want ErrPairingExpired", err)
	}
	if len(b.reg.invites) != 0 {
		t.Fatal("expired pairing still registered a mailbox")
	}
}

func TestPairingStartNeedsMailbox(t *testing.T) {
	a := newPairSide(t, newMemRooms(), "a", false)
	if _, _, err := a.svc.Start(context.Background()); !errors.Is(err, ErrOfflineForPairing) {
		t.Fatalf("err = %v", err)
	}
}

func TestPairingFinalizeValidation(t *testing.T) {
	ctx := context.Background()
	rooms := newMemRooms()
	a := newPairSide(t, rooms, "a", true)
	pendingA, _, err := a.svc.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.svc.Finalize(ctx, pendingA, "x", core.TrustAskFirst); !errors.Is(err, ErrPairingInProgress) {
		t.Fatalf("finalize before exchange err = %v", err)
	}
	if _, err := a.svc.Finalize(ctx, pendingA, "x", core.TrustLevel(7)); err == nil {
		t.Fatal("invalid trust accepted")
	}
	if _, err := a.svc.Finalize(ctx, "nope", "x", core.TrustAskFirst); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown pending err = %v", err)
	}
	if _, err := a.svc.Join(ctx, "not a code"); err == nil {
		t.Fatal("garbage code accepted")
	}
	awaitCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := a.svc.Await(awaitCtx, pendingA); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await without a joiner err = %v", err)
	}
}
