// Package relayclient implements the transport interfaces over relay-v1.
package relayclient

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/transport"
)

// Client holds what every relay-v1 connection needs: base URLs, HTTP client, clock.
type Client struct {
	origin string // scheme://host[:port]
	wsBase string // ws(s)://host[:port]
	http   *http.Client
	clock  core.Clock
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for blobs and WebSocket upgrades.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithClock sets the clock used for request-signature timestamps.
func WithClock(clk core.Clock) Option { return func(c *Client) { c.clock = clk } }

// New parses relayURL ("https://host" or "http://127.0.0.1:port"; no path, query, or fragment).
func New(relayURL string, opts ...Option) (*Client, error) {
	u, err := url.Parse(relayURL)
	if err != nil {
		return nil, fmt.Errorf("relay url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("relay url %q: scheme must be http or https", relayURL)
	}
	if u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("relay url %q: use scheme://host[:port] only", relayURL)
	}
	host := strings.ToLower(u.Host)
	c := &Client{
		origin: u.Scheme + "://" + host,
		wsBase: map[string]string{"http": "ws", "https": "wss"}[u.Scheme] + "://" + host,
		http:   http.DefaultClient,
		clock:  core.SystemClock{},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Origin is scheme://host[:port], the value bound into auth signatures.
func (c *Client) Origin() string { return c.origin }

// Dialer returns a transport.Dialer for mailbox connections.
func (c *Client) Dialer() transport.Dialer { return dialer{c: c} }

// Rooms returns a transport.Rooms for pairing rooms.
func (c *Client) Rooms() transport.Rooms { return rooms{c: c} }

// Blobs returns a transport.BlobStore whose requests are signed by s.
func (c *Client) Blobs(s transport.Signer) transport.BlobStore { return blobs{c: c, s: s} }
