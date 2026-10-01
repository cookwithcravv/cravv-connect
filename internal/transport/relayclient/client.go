// Package relayclient implements the transport interfaces over relay-v1.
package relayclient

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

// Client holds what every relay-v1 connection needs: base URLs, HTTP client, clock.
type Client struct {
	origin       string // scheme://host[:port]
	wsBase       string // ws(s)://host[:port]
	http         *http.Client
	clock        core.Clock
	pingInterval time.Duration
	pingTimeout  time.Duration
	reqTimeout   time.Duration
	dialTimeout  time.Duration
	blobTimeout  time.Duration
	recycleAfter time.Duration
	maxAge       time.Duration
}

// Keepalive defaults: relay-v1 clients SHOULD ping every 30 seconds.
const (
	DefaultPingInterval = 30 * time.Second
	DefaultPingTimeout  = 15 * time.Second
)

// DefaultRequestTimeout bounds how long a mailbox request (send, allow,
// deny, invite, room) waits for the relay's answer. A relay that does not
// answer in time is treated as a dead connection.
const DefaultRequestTimeout = 30 * time.Second

// Network timeouts. DefaultDialTimeout bounds a mailbox dial up to the
// WebSocket upgrade (the relay-v1 handshake after it has its own limit).
// DefaultBlobTimeout bounds one blob request, a 1 MiB chunk included, so a
// stalled transfer fails and is retried. DefaultResponseHeaderTimeout bounds
// the wait for any HTTP response's headers.
const (
	DefaultDialTimeout           = 30 * time.Second
	DefaultBlobTimeout           = 2 * time.Minute
	DefaultResponseHeaderTimeout = 30 * time.Second
)

// DefaultInternalRecycle is how old a mailbox connection must be for an
// internal answer to end it (ErrRecycled). A fresh connection that gets one
// stays up: its failure is not the depth limit a long-lived one can reach.
const DefaultInternalRecycle = 2 * time.Minute

// newHTTPClient is the default HTTP client: the standard transport with a
// bound on the wait for response headers.
func newHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = DefaultResponseHeaderTimeout
	return &http.Client{Transport: t}
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for blobs and WebSocket upgrades.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithKeepalive sets how often a mailbox connection sends a WebSocket ping and how
// long it waits for the pong. An unanswered ping ends the connection. interval <= 0
// disables pings.
func WithKeepalive(interval, timeout time.Duration) Option {
	return func(c *Client) { c.pingInterval, c.pingTimeout = interval, timeout }
}

// WithRequestTimeout sets how long a mailbox request waits for its answer
// before it fails with ErrRequestTimeout and the connection is ended.
// d <= 0 means no limit beyond the caller's context.
func WithRequestTimeout(d time.Duration) Option { return func(c *Client) { c.reqTimeout = d } }

// WithDialTimeout bounds a mailbox or pairing-room dial up to the WebSocket
// upgrade; d <= 0 means only the caller's context.
func WithDialTimeout(d time.Duration) Option { return func(c *Client) { c.dialTimeout = d } }

// WithBlobTimeout bounds each blob request; d <= 0 means only the caller's context.
func WithBlobTimeout(d time.Duration) Option { return func(c *Client) { c.blobTimeout = d } }

// WithInternalRecycle ends a mailbox connection at least d old when the relay
// answers one of its requests with internal, so the caller reconnects; the
// request still fails with that error. d < 0 never ends one.
func WithInternalRecycle(d time.Duration) Option { return func(c *Client) { c.recycleAfter = d } }

// WithMaxConnectionAge ends a mailbox connection d after it opened, with
// ErrRecycled, so the caller replaces it. d <= 0 means no limit.
func WithMaxConnectionAge(d time.Duration) Option { return func(c *Client) { c.maxAge = d } }

// WithClock sets the clock used for request-signature timestamps.
func WithClock(clk core.Clock) Option { return func(c *Client) { c.clock = clk } }

// New parses relayURL ("https://host" or "http://127.0.0.1:port"; no path, query, or
// fragment) and normalizes it with relayproto.NormalizeOrigin.
func New(relayURL string, opts ...Option) (*Client, error) {
	origin, err := relayproto.NormalizeOrigin(relayURL)
	if err != nil {
		return nil, fmt.Errorf("relay url: %w", err)
	}
	scheme, host, _ := strings.Cut(origin, "://")
	c := &Client{
		origin:       origin,
		wsBase:       map[string]string{"http": "ws", "https": "wss"}[scheme] + "://" + host,
		http:         newHTTPClient(),
		clock:        core.SystemClock{},
		pingInterval: DefaultPingInterval,
		pingTimeout:  DefaultPingTimeout,
		reqTimeout:   DefaultRequestTimeout,
		dialTimeout:  DefaultDialTimeout,
		blobTimeout:  DefaultBlobTimeout,
		recycleAfter: DefaultInternalRecycle,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// MaxConnectionAge is the WithMaxConnectionAge setting (0: no limit).
func (c *Client) MaxConnectionAge() time.Duration { return c.maxAge }

// Origin is the normalized scheme://host[:port], the value bound into auth and HTTP signatures.
func (c *Client) Origin() string { return c.origin }

// bounded returns ctx limited to d (d <= 0: ctx itself).
func bounded(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

// Dialer returns a transport.Dialer for mailbox connections.
func (c *Client) Dialer() transport.Dialer { return dialer{c: c} }

// Rooms returns a transport.Rooms for pairing rooms.
func (c *Client) Rooms() transport.Rooms { return rooms{c: c} }

// Blobs returns a transport.BlobStore whose requests are signed by s.
func (c *Client) Blobs(s transport.Signer) transport.BlobStore { return blobs{c: c, s: s} }
