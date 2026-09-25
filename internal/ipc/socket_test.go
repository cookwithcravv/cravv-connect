package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// shortDir returns a temp dir with a short path: unix socket paths are limited
// to about 104 bytes on macOS, and t.TempDir() paths can exceed that.
func shortDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "ipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func startServer(t *testing.T, s *Server) string {
	t.Helper()
	sock := filepath.Join(shortDir(t), "run", "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return sock
}

type echoParams struct {
	Text string `json:"text"`
}

func testServer(disconnected chan string) *Server {
	s := NewServer(Options{OnDisconnect: func(name string) { disconnected <- name }})
	s.Register("register", Typed(func(_ context.Context, cs *ConnState, p SessionRegisterParams) (any, error) {
		cs.SetSession(p.Agent+"@x", p.ProjectDir)
		return SessionRegisterResult{Name: p.Agent + "@x"}, nil
	}), GateNone)
	s.Register("echo", Typed(func(_ context.Context, _ *ConnState, p echoParams) (any, error) {
		return p, nil
	}), GateSession)
	s.Register("block", func(ctx context.Context, _ *ConnState, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, GateNone)
	s.Register("fail", func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return nil, core.ErrAlreadyClaimed
	}, GateNone)
	return s
}

func TestEndToEndOverSocket(t *testing.T) {
	disc := make(chan string, 4)
	sock := startServer(t, testServer(disc))
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Call(ctx, "echo", echoParams{"hi"}, nil); !errors.Is(err, core.ErrNoSession) {
		t.Fatalf("echo before register: %v", err)
	}
	var reg SessionRegisterResult
	if err := c.Call(ctx, "register", SessionRegisterParams{Agent: "claude", ProjectDir: "/p"}, &reg); err != nil || reg.Name != "claude@x" {
		t.Fatalf("register: %v %+v", err, reg)
	}
	var out echoParams
	if err := c.Call(ctx, "echo", echoParams{"hi"}, &out); err != nil || out.Text != "hi" {
		t.Fatalf("echo: %v %+v", err, out)
	}
	if err := c.Call(ctx, "fail", nil, nil); !errors.Is(err, core.ErrAlreadyClaimed) {
		t.Fatalf("fail: %v", err)
	}
	c.Close()
	select {
	case name := <-disc:
		if name != "claude@x" {
			t.Fatalf("disconnect name %q", name)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OnDisconnect not called")
	}
}

func TestNoDisconnectCallbackWithoutSession(t *testing.T) {
	disc := make(chan string, 1)
	sock := startServer(t, testServer(disc))
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Call(context.Background(), "fail", nil, nil); err == nil {
		t.Fatal("expected error")
	}
	c.Close()
	select {
	case name := <-disc:
		t.Fatalf("unexpected disconnect for %q", name)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestConcurrentCallsOnOneConnection(t *testing.T) {
	disc := make(chan string, 1)
	sock := startServer(t, testServer(disc))
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx := context.Background()
	if err := c.Call(ctx, "register", SessionRegisterParams{Agent: "a"}, nil); err != nil {
		t.Fatal(err)
	}
	bctx, bcancel := context.WithCancel(ctx)
	blocked := make(chan error, 1)
	go func() { blocked <- c.Call(bctx, "block", nil, nil) }()
	var out echoParams
	if err := c.Call(ctx, "echo", echoParams{"fast"}, &out); err != nil || out.Text != "fast" {
		t.Fatalf("echo while another call blocks: %v", err)
	}
	bcancel()
	if err := <-blocked; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked call: %v", err)
	}
}

func TestServerShutdownFailsPendingCall(t *testing.T) {
	s := testServer(make(chan string, 1))
	sock := filepath.Join(shortDir(t), "d.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Serve(ctx, ln); close(done) }()
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	res := make(chan error, 1)
	go func() { res <- c.Call(context.Background(), "block", nil, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-res:
		var re *RemoteError
		if !errors.Is(err, ErrClosed) && !errors.As(err, &re) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("call did not end on shutdown")
	}
	<-done
}

func TestParseErrorGetsNullIDResponse(t *testing.T) {
	sock := startServer(t, testServer(make(chan string, 1)))
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("this is not json\n"))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.ID) != "null" || resp.Error == nil || resp.Error.Code != CodeParseError {
		t.Fatalf("got %s", line)
	}
}

func TestDialWithoutDaemon(t *testing.T) {
	_, err := Dial(filepath.Join(shortDir(t), "missing.sock"))
	if !errors.Is(err, ErrDaemonNotRunning) {
		t.Fatalf("got %v", err)
	}
	if err.Error() != "daemon not running: run `cravv-connect daemon start`" {
		t.Fatalf("message %q", err.Error())
	}
}

func TestListenPermissionsAndStaleSocket(t *testing.T) {
	dir := filepath.Join(shortDir(t), "home")
	sock := filepath.Join(dir, "d.sock")

	// A stale socket file left behind by a crashed daemon.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	ln, err := Listen(sock)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	defer ln.Close()
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v %v", fi.Mode().Perm(), err)
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := Listen(sock); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen: %v", err)
	}
}

func TestListenRefusesNonSocket(t *testing.T) {
	sock := filepath.Join(shortDir(t), "d.sock")
	os.WriteFile(sock, []byte("x"), 0o600)
	if _, err := Listen(sock); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	if b, _ := os.ReadFile(sock); string(b) != "x" {
		t.Fatal("regular file was modified")
	}
}
