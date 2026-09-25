package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/store/sqlite"
)

// Fixtures shared by the Task 17-20 tests. The d2 prefix keeps them apart
// from the helpers of Tasks 13-16 in the same package.

var d2Epoch = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func d2Store(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func d2Peer(t *testing.T, st store.PeerStore, alias string, trust core.TrustLevel) (store.Peer, *keys.Identity) {
	t.Helper()
	id, err := keys.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	p := store.Peer{MachineID: id.MachineID(), IK: id.Public(), Alias: alias, TrustIn: trust, PairedAt: d2Epoch}
	if err := st.PutPeer(context.Background(), p); err != nil {
		t.Fatalf("put peer: %v", err)
	}
	return p, id
}

type d2Sent struct {
	ID          string
	To          core.MachineID
	Kind        core.Kind
	FromSession string
	ToSession   string
	Body        json.RawMessage
}

// d2Sender records envelopes instead of sending them.
type d2Sender struct {
	mu   sync.Mutex
	sent []d2Sent
	err  error
}

func (s *d2Sender) SendEnvelope(ctx context.Context, to core.MachineID, kind core.Kind, fromSession, toSession string, body any) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	id := core.NewID()
	s.sent = append(s.sent, d2Sent{ID: id, To: to, Kind: kind, FromSession: fromSession, ToSession: toSession, Body: b})
	return id, nil
}

func (s *d2Sender) ofKind(k core.Kind) []d2Sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []d2Sent
	for _, m := range s.sent {
		if m.Kind == k {
			out = append(out, m)
		}
	}
	return out
}

func d2Updates(t *testing.T, s *d2Sender) []core.TaskUpdateBody {
	t.Helper()
	var out []core.TaskUpdateBody
	for _, m := range s.ofKind(core.KindTaskUpdate) {
		var b core.TaskUpdateBody
		if err := json.Unmarshal(m.Body, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// d2Desktop records desktop notifications.
type d2Desktop struct {
	mu    sync.Mutex
	texts []string
}

func (d *d2Desktop) Notify(title, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.texts = append(d.texts, text)
}

func (d *d2Desktop) all() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.texts...)
}

func d2Env(t *testing.T, from store.Peer, kind core.Kind, fromSession, toSession string, body any) core.Envelope {
	t.Helper()
	env, err := core.NewEnvelope(core.NewFakeClock(d2Epoch), from.MachineID, "local", kind, body)
	if err != nil {
		t.Fatal(err)
	}
	env.FromSession, env.ToSession = fromSession, toSession
	return env
}

// d2Inbox wires a SessionRegistry and InboxService over one store.
func d2Inbox(t *testing.T, st *sqlite.DB, clock core.Clock) (*SessionRegistry, *InboxService) {
	t.Helper()
	reg := NewSessionRegistry(st, st, clock)
	return reg, NewInboxService(st, reg, st, clock)
}
