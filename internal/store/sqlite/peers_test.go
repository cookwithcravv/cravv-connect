package sqlite

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func testPeer(id, alias string) store.Peer {
	return store.Peer{
		MachineID: core.MachineID(id),
		IK:        ed25519.PublicKey(bytes.Repeat([]byte{id[0]}, ed25519.PublicKeySize)),
		Alias:     alias,
		Prekey:    core.SignedPrekeyWire{ID: "pk-" + id, Pub: []byte{1, 2, 3}, CreatedAt: t0.UnixMilli(), Sig: []byte{9}},
		RelayURL:  "https://relay.example.com",
		PairedAt:  t0,
	}
}

func TestPeerRoundTripAndUpsert(t *testing.T) {
	ctx := context.Background()
	ps := newTestDB(t)
	p := testPeer("aaaa", "gpu-box")
	if err := ps.PutPeer(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := ps.GetPeer(ctx, "aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if got.Alias != "gpu-box" || !bytes.Equal(got.IK, p.IK) ||
		got.Prekey.ID != "pk-aaaa" || !bytes.Equal(got.Prekey.Pub, []byte{1, 2, 3}) ||
		got.RelayURL != p.RelayURL || !got.PairedAt.Equal(t0) || got.Paused || got.PausedByPeer {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	p.Alias = "trainer"
	p.Paused = true
	p.PausedByPeer = true
	if err := ps.PutPeer(ctx, p); err != nil {
		t.Fatalf("upsert with new alias: %v", err)
	}
	got, err = ps.GetPeerByAlias(ctx, "trainer")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Paused || !got.PausedByPeer {
		t.Fatalf("upsert did not update fields: %+v", got)
	}
	if _, err := ps.GetPeerByAlias(ctx, "gpu-box"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("old alias lookup err = %v, want ErrNotFound", err)
	}
}

func TestPeerAliasUniqueness(t *testing.T) {
	ctx := context.Background()
	ps := newTestDB(t)
	if err := ps.PutPeer(ctx, testPeer("aaaa", "gpu-box")); err != nil {
		t.Fatal(err)
	}
	err := ps.PutPeer(ctx, testPeer("bbbb", "gpu-box"))
	if !errors.Is(err, store.ErrAliasTaken) {
		t.Fatalf("second peer with same alias: err = %v, want ErrAliasTaken", err)
	}
	// Re-putting the same peer with its own alias is not a conflict.
	if err := ps.PutPeer(ctx, testPeer("aaaa", "gpu-box")); err != nil {
		t.Fatalf("re-put same peer: %v", err)
	}
	if _, err := ps.GetPeer(ctx, "bbbb"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("rejected peer was stored: err = %v", err)
	}
}

func TestPeerListAndDelete(t *testing.T) {
	ctx := context.Background()
	ps := newTestDB(t)
	for _, p := range []store.Peer{testPeer("cccc", "zeta"), testPeer("aaaa", "alpha"), testPeer("bbbb", "mid")} {
		if err := ps.PutPeer(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	list, err := ps.ListPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Alias != "alpha" || list[1].Alias != "mid" || list[2].Alias != "zeta" {
		t.Fatalf("ListPeers order = %+v", list)
	}
	if err := ps.DeletePeer(ctx, "bbbb"); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.GetPeer(ctx, "bbbb"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("deleted peer: err = %v, want ErrNotFound", err)
	}
	// The alias of a deleted peer is free again.
	if err := ps.PutPeer(ctx, testPeer("dddd", "mid")); err != nil {
		t.Fatalf("reuse alias of deleted peer: %v", err)
	}
}
