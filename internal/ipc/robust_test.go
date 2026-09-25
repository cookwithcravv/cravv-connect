package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResponsesAreNotHTMLEscaped(t *testing.T) {
	s := NewServer(Options{})
	s.Register("lt", func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return map[string]string{"s": "<&>"}, nil
	}, GateNone)
	sock := startServer(t, s)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"lt"}` + "\n"))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, `"s":"<&>"`) {
		t.Fatalf("response is HTML-escaped: %s", line)
	}
}

func TestOversizeResponseBecomesTooLargeError(t *testing.T) {
	s := NewServer(Options{})
	s.Register("huge", func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return map[string]string{"s": strings.Repeat("x", MaxLineBytes)}, nil
	}, GateNone)
	s.Register("small", func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}, GateNone)
	c, err := Dial(startServer(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Call(context.Background(), "huge", nil, nil)
	if !IsKind(err, KindTooLarge) {
		t.Fatalf("huge: err = %v, want kind too_large", err)
	}
	var ok map[string]bool
	if err := c.Call(context.Background(), "small", nil, &ok); err != nil || !ok["ok"] {
		t.Fatalf("stream broken after an oversize response: %v", err)
	}
}

func TestCancelNotificationCancelsHandler(t *testing.T) {
	s := NewServer(Options{})
	started := make(chan struct{})
	ended := make(chan error, 1)
	s.Register("block", func(ctx context.Context, _ *ConnState, _ json.RawMessage) (any, error) {
		close(started)
		select {
		case <-ctx.Done():
			ended <- ctx.Err()
		case <-time.After(10 * time.Second):
			ended <- errors.New("handler was not cancelled")
		}
		return nil, ctx.Err()
	}, GateNone)
	s.Register("small", func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}, GateNone)
	c, err := Dial(startServer(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	callErr := make(chan error, 1)
	go func() { callErr <- c.Call(ctx, "block", nil, nil) }()
	<-started
	cancel()
	if err := <-callErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("Call err = %v", err)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler still running after the client cancelled")
	}
	// The connection stays usable.
	if err := c.Call(context.Background(), "small", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestInflightCapReturnsBusy(t *testing.T) {
	s := NewServer(Options{})
	release := make(chan struct{})
	var once sync.Once
	s.Register("block", func(ctx context.Context, _ *ConnState, _ json.RawMessage) (any, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, nil
	}, GateNone)
	c, err := Dial(startServer(t, s))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	defer once.Do(func() { close(release) })
	errs := make(chan error, MaxInflight+1)
	for range MaxInflight {
		go func() { errs <- c.Call(context.Background(), "block", nil, nil) }()
	}
	// Wait until all MaxInflight calls are in flight on the server.
	deadline := time.Now().Add(5 * time.Second)
	for s.inflightForTest() < MaxInflight {
		if time.Now().After(deadline) {
			t.Fatalf("only %d calls in flight", s.inflightForTest())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.Call(context.Background(), "block", nil, nil); !IsKind(err, KindBusy) {
		t.Fatalf("call over the cap: err = %v, want kind busy", err)
	}
	once.Do(func() { close(release) })
	for range MaxInflight {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestTooLongRequestGetsErrorBeforeClose(t *testing.T) {
	sock := startServer(t, NewServer(Options{}))
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() {
		big := `{"jsonrpc":"2.0","id":1,"method":"x","params":"` + strings.Repeat("a", MaxLineBytes) + "\"}\n"
		conn.Write([]byte(big))
	}()
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("no error reply before close: %v", err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil || resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.Kind != KindTooLarge {
		t.Fatalf("reply = %s", line)
	}
}

func (s *Server) inflightForTest() int { return int(s.active.Load()) }
