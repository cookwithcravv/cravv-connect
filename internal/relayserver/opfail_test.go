package relayserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// failEnqueue is a Backend whose Enqueue fails for one recipient mailbox.
type failEnqueue struct {
	Backend
	to string
}

func (f failEnqueue) Enqueue(ctx context.Context, mailbox string, from ed25519.PublicKey, id string, frame []byte, lim QueueLimits) (uint64, error) {
	if mailbox == f.to {
		return 0, errors.New("storage unavailable")
	}
	return f.Backend.Enqueue(ctx, mailbox, from, id, frame, lim)
}

// A request that fails inside the relay gets res{error, internal}; the connection stays
// open and keeps receiving deliveries and processing acks (the same contract as relay-cf).
func TestFailedRequestKeepsConnection(t *testing.T) {
	ka, kb, kx := newKey(t), newKey(t), newKey(t)
	tr := newTestRelayBackend(t, nil, func(be Backend) Backend {
		return failEnqueue{Backend: be, to: relayproto.MailboxID(pubOf(kb))}
	})
	a, b, x := tr.member(t, ka), tr.member(t, kb), tr.member(t, kx)
	b.allow(ka)
	a.allow(kx)

	res := a.req(relayproto.Send{T: relayproto.TypeSend, RID: "lost", To: relayproto.MailboxID(pubOf(kb)), ID: "lost", Frame: relayproto.B64([]byte("x"))})
	if res["rid"] != "lost" || res["status"] != relayproto.StatusError || res["code"] != relayproto.CodeInternal {
		t.Fatalf("send with a failing enqueue: %v, want error internal", res)
	}
	if st := x.send(ka, "after", []byte("after")); st != relayproto.StatusQueued {
		t.Fatalf("send to a: %s", st)
	}
	d := a.expect(relayproto.TypeDeliver)
	if d["id"] != "after" {
		t.Fatalf("deliver = %v", d)
	}
	a.write(relayproto.Ack{T: relayproto.TypeAck, Seq: uint64(d["seq"].(float64))})
	if res := a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "i"}); res["status"] != relayproto.StatusOK {
		t.Fatalf("request after the failure: %v", res)
	}
	// The ack was processed: a new connection gets no backlog.
	a2 := tr.member(t, ka)
	if m, err := a2.tryRead(200 * time.Millisecond); err == nil {
		t.Fatalf("acked frame delivered again: %v", m)
	}
}
