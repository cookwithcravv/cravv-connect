package relayclient

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// mailbox is a live relay-v1 mailbox connection. One reader goroutine owns all reads;
// requests are matched to replies by rid.
//
// The reader never blocks on the Deliveries consumer: deliver frames go into an
// unbounded in-memory queue that a forwarding goroutine feeds into the channel, so res
// frames keep flowing however slowly deliveries are drained. The relay's queue caps
// (10000 frames, 50 MB) bound the backlog one connection can push. When the
// connection ends, frames still in the queue are dropped; they were not acked, so the
// relay pushes them again on the next connection.
type mailbox struct {
	ws         *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	deliveries chan transport.Delivery
	done       chan struct{}
	fwdDone    chan struct{}

	reqTimeout time.Duration // per request; 0 means none

	mu      sync.Mutex
	nextRID uint64
	pending map[string]chan relayproto.Res
	err     error

	qmu     sync.Mutex
	qcond   *sync.Cond
	queue   []transport.Delivery
	qclosed bool
}

func newMailbox(ws *websocket.Conn, pingInterval, pingTimeout, reqTimeout time.Duration) *mailbox {
	ctx, cancel := context.WithCancel(context.Background())
	m := &mailbox{
		ws:         ws,
		reqTimeout: reqTimeout,
		ctx:        ctx,
		cancel:     cancel,
		deliveries: make(chan transport.Delivery),
		done:       make(chan struct{}),
		fwdDone:    make(chan struct{}),
		pending:    map[string]chan relayproto.Res{},
	}
	m.qcond = sync.NewCond(&m.qmu)
	go m.forward()
	go m.readLoop()
	if pingInterval > 0 {
		go m.pingLoop(pingInterval, pingTimeout)
	}
	return m
}

// fail records err as the reason the connection ended, unless one is already set.
func (m *mailbox) fail(err error) {
	m.mu.Lock()
	if m.err == nil {
		m.err = err
	}
	m.mu.Unlock()
}

func (m *mailbox) readLoop() {
	m.fail(m.readFrames())
	m.cancel()
	m.ws.CloseNow()
	m.qmu.Lock()
	m.qclosed = true
	m.queue = nil
	m.qcond.Broadcast()
	m.qmu.Unlock()
	<-m.fwdDone
	close(m.done)
}

// enqueue hands a delivery to the forwarder without blocking.
func (m *mailbox) enqueue(d transport.Delivery) {
	m.qmu.Lock()
	m.queue = append(m.queue, d)
	m.qcond.Signal()
	m.qmu.Unlock()
}

// forward moves queued deliveries into the Deliveries channel, in order, and closes
// the channel when the connection ends.
func (m *mailbox) forward() {
	defer close(m.fwdDone)
	defer close(m.deliveries)
	for {
		m.qmu.Lock()
		for len(m.queue) == 0 && !m.qclosed {
			m.qcond.Wait()
		}
		if m.qclosed {
			m.qmu.Unlock()
			return
		}
		d := m.queue[0]
		m.queue[0] = transport.Delivery{}
		m.queue = m.queue[1:]
		m.qmu.Unlock()
		select {
		case m.deliveries <- d:
		case <-m.ctx.Done():
			return
		}
	}
}

// pingLoop sends a WebSocket ping every interval. A ping that gets no pong within
// timeout means the connection is dead: it is closed and Err reports why.
func (m *mailbox) pingLoop(interval, timeout time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(m.ctx, timeout)
		err := m.ws.Ping(pctx)
		cancel()
		if err != nil {
			if m.ctx.Err() == nil {
				m.fail(fmt.Errorf("relay: keepalive ping failed: %w", err))
			}
			m.cancel()
			m.ws.CloseNow()
			return
		}
	}
}

func (m *mailbox) readFrames() error {
	for {
		h, raw, err := readFrame(m.ctx, m.ws)
		if err != nil {
			return fmt.Errorf("relay: connection lost: %w", err)
		}
		switch h.T {
		case relayproto.TypeRes:
			var r relayproto.Res
			if err := json.Unmarshal(raw, &r); err != nil {
				return fmt.Errorf("%w: %v", ErrProtocol, err)
			}
			m.resolve(r)
		case relayproto.TypeDeliver:
			d, err := parseDeliver(raw)
			if err != nil {
				return err
			}
			m.enqueue(d)
		case relayproto.TypeError:
			return serverError(raw)
		}
	}
}

func parseDeliver(raw []byte) (transport.Delivery, error) {
	var d relayproto.Deliver
	if err := json.Unmarshal(raw, &d); err != nil {
		return transport.Delivery{}, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	from, err := relayproto.ParseIK(d.From)
	if err != nil {
		return transport.Delivery{}, fmt.Errorf("%w: deliver from: %v", ErrProtocol, err)
	}
	frame, err := relayproto.UnB64(d.Frame)
	if err != nil {
		return transport.Delivery{}, fmt.Errorf("%w: deliver frame: %v", ErrProtocol, err)
	}
	return transport.Delivery{Seq: d.Seq, From: from, ID: d.ID, Frame: frame}, nil
}

func (m *mailbox) resolve(r relayproto.Res) {
	m.mu.Lock()
	ch := m.pending[r.RID]
	delete(m.pending, r.RID)
	m.mu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// request sends a frame built for a fresh rid and waits for its res, at most
// reqTimeout: a relay that does not answer in time is a dead connection, so
// it is ended (and Err reports ErrRequestTimeout) for the caller to redial.
func (m *mailbox) request(ctx context.Context, build func(rid string) any) (relayproto.Res, error) {
	ch := make(chan relayproto.Res, 1)
	m.mu.Lock()
	if m.err != nil {
		err := m.err
		m.mu.Unlock()
		return relayproto.Res{}, err
	}
	m.nextRID++
	rid := strconv.FormatUint(m.nextRID, 10)
	m.pending[rid] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, rid)
		m.mu.Unlock()
	}()
	if err := writeJSON(m.ctx, m.ws, build(rid)); err != nil {
		return relayproto.Res{}, fmt.Errorf("relay: write: %w", err)
	}
	var timeout <-chan time.Time
	if m.reqTimeout > 0 {
		t := time.NewTimer(m.reqTimeout)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-timeout:
		err := fmt.Errorf("%w (%s)", ErrRequestTimeout, m.reqTimeout)
		m.fail(err)
		m.cancel()
		m.ws.CloseNow()
		return relayproto.Res{}, err
	case r := <-ch:
		if r.Status == relayproto.StatusError {
			return r, &ServerError{Code: r.Code}
		}
		return r, nil
	case <-ctx.Done():
		return relayproto.Res{}, ctx.Err()
	case <-m.done:
		return relayproto.Res{}, m.Err()
	}
}

func (m *mailbox) Send(ctx context.Context, to core.MachineID, id string, frame []byte) (transport.SendStatus, error) {
	r, err := m.request(ctx, func(rid string) any {
		return relayproto.Send{T: relayproto.TypeSend, RID: rid, To: string(to), ID: id, Frame: relayproto.B64(frame)}
	})
	if err != nil {
		return "", err
	}
	return transport.SendStatus(r.Status), nil
}

// Deliveries yields pushed frames in the order received. It never makes the reader
// wait: undrained frames are buffered in memory. It is closed, dropping any frames not
// yet received (they are redelivered on the next connection), before Done closes.
func (m *mailbox) Deliveries() <-chan transport.Delivery { return m.deliveries }

// Ack writes on the connection's own context so a cancelled caller ctx cannot
// tear down the connection mid-write.
func (m *mailbox) Ack(ctx context.Context, seq uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-m.done:
		return m.Err()
	default:
	}
	return writeJSON(m.ctx, m.ws, relayproto.Ack{T: relayproto.TypeAck, Seq: seq})
}

func (m *mailbox) okRequest(ctx context.Context, build func(rid string) any) (relayproto.Res, error) {
	r, err := m.request(ctx, build)
	if err != nil {
		return r, err
	}
	if r.Status != relayproto.StatusOK {
		return r, &ServerError{Code: r.Status}
	}
	return r, nil
}

func (m *mailbox) Allow(ctx context.Context, ik ed25519.PublicKey) error {
	_, err := m.okRequest(ctx, func(rid string) any {
		return relayproto.Allow{T: relayproto.TypeAllow, RID: rid, IK: relayproto.B64(ik)}
	})
	return err
}

func (m *mailbox) Deny(ctx context.Context, ik ed25519.PublicKey) error {
	_, err := m.okRequest(ctx, func(rid string) any {
		return relayproto.Deny{T: relayproto.TypeDeny, RID: rid, IK: relayproto.B64(ik)}
	})
	return err
}

func (m *mailbox) RequestInvite(ctx context.Context) (string, error) {
	r, err := m.okRequest(ctx, func(rid string) any {
		return relayproto.InviteRequest{T: relayproto.TypeInviteRequest, RID: rid}
	})
	return r.Invite, err
}

func (m *mailbox) CreateRoom(ctx context.Context) (string, string, error) {
	r, err := m.okRequest(ctx, func(rid string) any {
		return relayproto.RoomCreate{T: relayproto.TypeRoomCreate, RID: rid}
	})
	return r.Nameplate, r.CreatorToken, err
}

func (m *mailbox) Done() <-chan struct{} { return m.done }

func (m *mailbox) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Close ends the connection; Err then reports ErrClosed.
func (m *mailbox) Close() error {
	m.mu.Lock()
	if m.err == nil {
		m.err = ErrClosed
	}
	m.mu.Unlock()
	_ = m.ws.Close(websocket.StatusNormalClosure, "")
	m.cancel()
	<-m.done
	return nil
}
