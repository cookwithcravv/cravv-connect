package ipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A Server may be served again while a previous Serve is still draining its
// connections (a restart that reuses the Server). Each Serve waits only for
// its own connections, so the second Serve's accepts never race the first
// Serve's wait. Run with -race.
func TestServeAgainWhileDraining(t *testing.T) {
	dir, err := os.MkdirTemp("", "ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	s := NewServer(Options{})

	for round := 0; round < 20; round++ {
		ln, err := Listen(sock)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { s.Serve(ctx, ln); close(done) }()

		// A connection that stays open keeps this Serve draining after cancel.
		c, err := Dial(sock)
		if err != nil {
			t.Fatal(err)
		}
		cancel()

		// Serve again on a fresh listener as soon as the old one has closed,
		// while the first Serve is still waiting for its open connection.
		ln2 := listenWhenFree(t, sock)
		ctx2, cancel2 := context.WithCancel(context.Background())
		done2 := make(chan struct{})
		go func() { s.Serve(ctx2, ln2); close(done2) }()
		c2, err := Dial(sock)
		if err != nil {
			t.Fatal(err)
		}

		c.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("first Serve did not return after its connection closed")
		}
		c2.Close()
		cancel2()
		select {
		case <-done2:
		case <-time.After(5 * time.Second):
			t.Fatal("second Serve did not return")
		}
	}
}

// listenWhenFree retries Listen until the previous listener on sock has
// closed (it closes asynchronously when its Serve is cancelled).
func listenWhenFree(t *testing.T, sock string) net.Listener {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ln, err := Listen(sock)
		if err == nil {
			return ln
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}
