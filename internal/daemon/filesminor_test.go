package daemon

import (
	"context"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// A declined notice that fails to reach the inbox is retried, and the
// redelivered offer writes it.
func TestHandleOfferNoticeIsRetrySafe(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 1) // every file is over the quota
	flaky := &flakyInbox{InboxStore: e.te.st}
	e.files.d.Inbox = NewInboxService(flaky, e.te.shared, e.te.st, e.te.st, e.te.clock)
	e.te.inbox = e.files.d.Inbox
	body := e.blobs.put(t, "a.txt", []byte("ab"))
	env := d2Env(t, e.te.peer, core.KindFileOffer, e.te.link.ID, body)
	handleRetry(t, flaky, func() error { return e.files.HandleOffer(withLink(ctx, e.te.link), e.te.peer, env) })
	if ok, _ := e.te.st.HasInboxMsg(ctx, env.ID); !ok {
		t.Fatal("declined notice never delivered")
	}
	if r := e.record(t, body.FileID); r.State != store.FileDeclined {
		t.Fatalf("state %s", r.State)
	}
}

// List never exposes file keys.
func TestFileListHasNoKeys(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	e.offer(t, e.blobs.put(t, "a.txt", []byte("a")))
	recs, err := e.files.List(ctx)
	if err != nil || len(recs) != 1 {
		t.Fatalf("List = %+v, %v", recs, err)
	}
	if recs[0].Key != nil {
		t.Fatal("List returned a file key")
	}
}
