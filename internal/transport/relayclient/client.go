// Package relayclient implements the transport interfaces over relay-v1.
package relayclient

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/relayproto"
	"github.com/cravv/cravv-connect/internal/transport"
)

// Client holds what every relay-v1 connection needs: base URLs, HTTP client, clock.
type Client struct {
	origin       string // scheme://host[:port]
	wsBase       string // ws(s)://host[:port]
	http         *http.Client
	clock        core.Clock
	pingInterval time.Duration
	pingTimeout  time.Duration
}

// Keepalive defaults: relay-v1 clients SHOULD ping every 30 seconds.
const (
	DefaultPingInterval = 30 * time.Second
	DefaultPingTimeout  = 15 * time.Second
)

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
		http:         http.DefaultClient,
		clock:        core.SystemClock{},
		pingInterval: DefaultPingInterval,
		pingTimeout:  DefaultPingTimeout,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Origin is the normalized scheme://host[:port], the value bound into auth and HTTP signatures.
func (c *Client) Origin() string { return c.origin }

// Dialer returns a transport.Dialer for mailbox connections.
func (c *Client) Dialer() transport.Dialer { return dialer{c: c} }

// Rooms returns a transport.Rooms for pairing rooms.
func (c *Client) Rooms() transport.Rooms { return rooms{c: c} }

// Blobs returns a transport.BlobStore whose requests are signed by s.
func (c *Client) Blobs(s transport.Signer) transport.BlobStore { return blobs{c: c, s: s} }
