package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestCheckPeerUID(t *testing.T) {
	if err := checkPeerUID(501, nil, 501); err != nil {
		t.Fatalf("same uid rejected: %v", err)
	}
	if err := checkPeerUID(0, nil, 501); err == nil {
		t.Fatal("root peer accepted by a user daemon")
	}
	if err := checkPeerUID(502, nil, 501); err == nil {
		t.Fatal("other user accepted")
	}
	if err := checkPeerUID(501, errors.New("getsockopt failed"), 501); err == nil {
		t.Fatal("unknown peer accepted")
	}
}

func TestPeerUIDOfSameUserConnection(t *testing.T) {
	sock := shortDir(t) + "/p.sock"
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	srv := <-accepted
	defer srv.Close()
	uid, err := peerUID(srv)
	if err != nil || uid != os.Getuid() {
		t.Fatalf("peerUID = %d, %v; want %d", uid, err, os.Getuid())
	}
}

// A connection from another user is closed before any request is read.
func TestServeRejectsOtherUser(t *testing.T) {
	orig := selfUID
	selfUID = func() int { return os.Getuid() + 1 } // pretend the daemon runs as someone else
	t.Cleanup(func() { selfUID = orig })
	s := NewServer(Options{})
	s.Register("ping", func(context.Context, *ConnState, json.RawMessage) (any, error) { return "pong", nil }, 0)
	sock := startServer(t, s)
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Call(ctx, "ping", nil, nil); err == nil {
		t.Fatal("call from another uid succeeded")
	}
}
