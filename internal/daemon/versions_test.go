package daemon

import (
	"context"
	"testing"

	"github.com/cravv/cravv-connect/internal/store"
)

type nopGateReplier struct{}

func (nopGateReplier) UnknownLink(context.Context, store.Peer, string) {}
func (nopGateReplier) Unsupported(context.Context, store.Peer)         {}

// A peer marked outdated for v1 traffic is cleared once valid link traffic
// arrives from it (it upgraded); other peers keep their notice.
func TestVersionNoticesClearOnLinkTraffic(t *testing.T) {
	v := NewVersionNotices()
	r := v.Replier(nopGateReplier{})
	old := store.Peer{MachineID: "m-old", Alias: "old-mac"}
	other := store.Peer{MachineID: "m-other", Alias: "other-mac"}
	r.Unsupported(context.Background(), old)
	r.Unsupported(context.Background(), other)
	if got := v.Errors(); len(got) != 2 {
		t.Fatalf("errors %v", got)
	}
	v.Seen(old)
	got := v.Errors()
	if len(got) != 1 || got[0] != "other-mac runs an older cravv-connect without session links: it needs an upgrade" {
		t.Fatalf("errors after old-mac upgraded: %v", got)
	}
}
