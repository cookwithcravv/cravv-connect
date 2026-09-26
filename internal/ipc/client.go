package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
)

// Client is a connection to the daemon. It is safe for concurrent Calls;
// responses are matched to requests by id.
type Client struct {
	conn    net.Conn
	wmu     sync.Mutex
	mu      sync.Mutex
	pending map[uint64]chan Response
	next    atomic.Uint64
	done    chan struct{}
}

// Dial connects to the daemon socket.
func Dial(socketPath string) (*Client, error) {
	return DialContext(context.Background(), socketPath)
}

// DialContext connects to the daemon socket. A missing socket or a refused
// connection returns ErrDaemonNotRunning.
func DialContext(ctx context.Context, socketPath string) (*Client, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
			return nil, ErrDaemonNotRunning
		}
		return nil, fmt.Errorf("%w (%v)", ErrDaemonNotRunning, err)
	}
	return NewClient(conn), nil
}

func (c *Client) readLoop() {
	defer close(c.done)
	sc := bufio.NewScanner(c.conn)
	sc.Buffer(make([]byte, 64<<10), MaxLineBytes)
	for sc.Scan() {
		var resp Response
		if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
			continue
		}
		id, err := strconv.ParseUint(string(resp.ID), 10, 64)
		if err != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ok {
			ch <- resp
		}
	}
}

// Done is closed when the connection ends.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close closes the connection. The daemon then ends this connection's session.
func (c *Client) Close() error { return c.conn.Close() }

// Call sends one request and waits for its response. params nil sends {}.
// result may be nil to discard the result. Daemon errors come back as
// *RemoteError, which matches core sentinel errors with errors.Is.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	if params == nil {
		params = Empty{}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	id := c.next.Add(1)
	ch := make(chan Response, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	line, err := json.Marshal(Request{JSONRPC: Version, ID: json.RawMessage(strconv.FormatUint(id, 10)), Method: method, Params: raw})
	if err != nil {
		return err
	}
	c.wmu.Lock()
	_, err = c.conn.Write(append(line, '\n'))
	c.wmu.Unlock()
	if err != nil {
		return ErrClosed
	}

	var resp Response
	select {
	case resp = <-ch:
	case <-c.done:
		select {
		case resp = <-ch:
		default:
			return ErrClosed
		}
	case <-ctx.Done():
		c.sendCancel(id)
		return ctx.Err()
	}
	if resp.Error != nil {
		return fromWire(resp.Error)
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("decode result of %s: %w", method, err)
		}
	}
	return nil
}

// sendCancel tells the daemon to stop request id (best effort): a cancelled
// inbox.wait then leaves its items unread instead of handing them to a reply
// nobody reads.
func (c *Client) sendCancel(id uint64) {
	params, _ := json.Marshal(CancelParams{ID: json.RawMessage(strconv.FormatUint(id, 10))})
	line, err := json.Marshal(Request{JSONRPC: Version, Method: MethodCancel, Params: params})
	if err != nil {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _ = c.conn.Write(append(line, '\n'))
}
