package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cravv/cravv-connect/internal/audit"
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

// Two concurrent accepts of one held file: exactly one wins.
func TestAcceptIsAtomic(t *testing.T) {
	ctx := context.Background()
	for round := 0; round < 5; round++ {
		e := d2FileSvc(t, 0)
		body := e.blobs.put(t, "a.txt", []byte("a"))
		e.hold(t, body.FileID, body)
		var wg sync.WaitGroup
		var ok atomic.Int32
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := e.files.Accept(ctx, body.FileID, true); err == nil {
					ok.Add(1)
				} else if !errors.Is(err, core.ErrBadTransition) {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		e.files.Wait()
		if ok.Load() != 1 || len(e.te.audit.ofType(audit.EvFileAccept)) != 1 {
			t.Fatalf("round %d: %d accepts succeeded, %d audited", round, ok.Load(), len(e.te.audit.ofType(audit.EvFileAccept)))
		}
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
