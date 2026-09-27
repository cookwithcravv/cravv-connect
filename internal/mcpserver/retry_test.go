package mcpserver

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// flakyConn fails every call but session.register with ipc.ErrClosed while
// broken, as a connection does when the daemon goes away mid-call.
type flakyConn struct {
	broken bool
	log    *[]string
	mu     *sync.Mutex
	done   chan struct{}
}

func (c *flakyConn) Call(_ context.Context, method string, _, _ any) error {
	c.mu.Lock()
	*c.log = append(*c.log, method)
	c.mu.Unlock()
	if method != ipc.MethodSessionRegister && c.broken {
		return ipc.ErrClosed
	}
	return nil
}
func (c *flakyConn) Close() error          { return nil }
func (c *flakyConn) Done() <-chan struct{} { return c.done }

func flakySession() (*Session, *[]string, *int) {
	var log []string
	var mu sync.Mutex
	dials := 0
	s := NewSession(func(context.Context) (Conn, error) {
		dials++
		// The first connection breaks; later ones work.
		return &flakyConn{broken: dials == 1, log: &log, mu: &mu, done: make(chan struct{})}, nil
	}, "/w/p")
	return s, &log, &dials
}

func count(log []string, method string) int {
	n := 0
	for _, m := range log {
		if m == method {
			n++
		}
	}
	return n
}

func TestNonIdempotentCallsAreNotRetried(t *testing.T) {
	for _, method := range []string{
		ipc.MethodChatSend, ipc.MethodTaskCreate, ipc.MethodTaskUpdate, ipc.MethodTaskComplete,
		ipc.MethodTaskFail, ipc.MethodTaskCancel, ipc.MethodTaskClaim, ipc.MethodFileSend,
		ipc.MethodPeerPause, ipc.MethodPeerUnpair, ipc.MethodKill,
		ipc.MethodInboxCheck, ipc.MethodInboxWait, ipc.MethodSessionShare, ipc.MethodLinkConnect,
		ipc.MethodLinkDisconnect, ipc.MethodLinkRestrict,
	} {
		s, log, dials := flakySession()
		err := s.Call(context.Background(), method, nil, nil)
		if !errors.Is(err, ipc.ErrClosed) {
			t.Fatalf("%s: err = %v, want ErrClosed", method, err)
		}
		if n := count(*log, method); n != 1 || *dials != 1 {
			t.Fatalf("%s: sent %d times over %d connections, want once", method, n, *dials)
		}
		// The next call reconnects.
		if err := s.Call(context.Background(), ipc.MethodStatus, nil, nil); err != nil || *dials != 2 {
			t.Fatalf("%s: next call err %v, dials %d", method, err, *dials)
		}
	}
}

func TestIdempotentReadsAreRetried(t *testing.T) {
	for _, method := range []string{ipc.MethodStatus, ipc.MethodPeerList, ipc.MethodTaskGet, ipc.MethodFilesList, ipc.MethodHookCounts,
		ipc.MethodLinks, ipc.MethodSessionsList} {
		s, log, dials := flakySession()
		if err := s.Call(context.Background(), method, nil, nil); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		if n := count(*log, method); n != 2 || *dials != 2 {
			t.Fatalf("%s: sent %d times over %d connections, want a retry", method, n, *dials)
		}
	}
}
