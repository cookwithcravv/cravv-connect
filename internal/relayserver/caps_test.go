package relayserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

func TestInviteCapPerMember(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	a := tr.member(t, newKey(t))
	for i := range 20 {
		res := a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: fmt.Sprint(i)})
		if res["status"] != relayproto.StatusOK {
			t.Fatalf("invite %d: %v", i+1, res)
		}
	}
	res := a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "21"})
	if res["status"] != relayproto.StatusError || res["code"] != relayproto.CodeRateLimited {
		t.Fatalf("21st invite: %v, want error rate_limited", res)
	}
	b := tr.member(t, newKey(t))
	if res := b.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "b"}); res["status"] != relayproto.StatusOK {
		t.Fatalf("cap is per member: %v", res)
	}
	tr.clock.Advance(10 * time.Minute)
	if res := a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "after"}); res["status"] != relayproto.StatusOK {
		t.Fatalf("expired invites still counted: %v", res)
	}
}

func TestRoomCapPerMember(t *testing.T) {
	tr := newTestRelay(t, Limits{MaxRoomsPerMember: 3})
	a := tr.member(t, newKey(t))
	for i := range 3 {
		if res := a.req(relayproto.RoomCreate{T: relayproto.TypeRoomCreate, RID: fmt.Sprint(i)}); res["status"] != relayproto.StatusOK {
			t.Fatalf("room %d: %v", i+1, res)
		}
	}
	res := a.req(relayproto.RoomCreate{T: relayproto.TypeRoomCreate, RID: "x"})
	if res["status"] != relayproto.StatusError || res["code"] != relayproto.CodeRateLimited {
		t.Fatalf("4th room: %v, want error rate_limited", res)
	}
	tr.clock.Advance(10 * time.Minute)
	if res := a.req(relayproto.RoomCreate{T: relayproto.TypeRoomCreate, RID: "y"}); res["status"] != relayproto.StatusOK {
		t.Fatalf("expired rooms still counted: %v", res)
	}
}

func TestDefaultRoomCapIsEight(t *testing.T) {
	if got := DefaultLimits().MaxRoomsPerMember; got != 8 {
		t.Fatalf("MaxRoomsPerMember = %d, want 8", got)
	}
	if got := DefaultLimits().MaxInvitesPerMember; got != 20 {
		t.Fatalf("MaxInvitesPerMember = %d, want 20", got)
	}
	if got := DefaultLimits().TotalBlobBytes; got != 50<<30 {
		t.Fatalf("TotalBlobBytes = %d, want 50 GiB", got)
	}
}

func TestBlobRelayWideCap(t *testing.T) {
	tr := newTestRelay(t, Limits{TotalBlobBytes: 100})
	a, b := newKey(t), newKey(t)
	tr.member(t, a)
	tr.member(t, b)
	if code, _ := tr.createBlob(t, a, b, 60, 1); code != http.StatusCreated {
		t.Fatalf("first = %d", code)
	}
	code, body := tr.createBlob(t, b, a, 50, 1)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over relay-wide cap = %d %s", code, body)
	}
}

func TestSweeperPurgesAndStops(t *testing.T) {
	tr := newTestRelayCfg(t, func(c *Config) { c.SweepInterval = 5 * time.Millisecond })
	a := tr.member(t, newKey(t))
	if res := a.req(relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: "1"}); res["status"] != relayproto.StatusOK {
		t.Fatal(res)
	}
	mem := tr.srv.be.(*memoryBackend)
	count := func() int {
		mem.mu.Lock()
		defer mem.mu.Unlock()
		return len(mem.invites)
	}
	if count() != 1 {
		t.Fatalf("invites = %d", count())
	}
	tr.clock.Advance(10 * time.Minute)
	deadline := time.Now().Add(2 * time.Second)
	for count() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("sweeper did not purge the expired invite")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tr.srv.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-tr.srv.sweepDone:
	default:
		t.Fatal("Close returned before the sweeper stopped")
	}
	_ = tr.srv.Close() // idempotent
}

func TestSweepEvictsRateLimiterBuckets(t *testing.T) {
	tr := newTestRelay(t, Limits{})
	tr.srv.limiter.allow("m")
	tr.srv.ipLimiter.allow("1.2.3.4")
	tr.clock.Advance(time.Hour)
	tr.srv.sweep(context.Background())
	for _, l := range []*rateLimiter{tr.srv.limiter, tr.srv.ipLimiter} {
		l.mu.Lock()
		n := len(l.buckets)
		l.mu.Unlock()
		if n != 0 {
			t.Fatalf("%d idle buckets left after sweep", n)
		}
	}
}
