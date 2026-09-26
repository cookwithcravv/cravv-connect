// Package relayaddr is the rule for relay URLs that come from somewhere other
// than the person at this machine's keyboard: a peer's pairing payload,
// control.relay_moved, or a join code typed or scanned from another machine.
package relayaddr

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// cgnat is the carrier-grade NAT range that Tailscale uses for node addresses.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Check accepts https with a host, or plain http only for relays on this
// machine or a private network (loopback, RFC 1918 and unique local
// addresses, Tailscale's 100.64.0.0/10, and single-label mDNS ".local"
// names). The relay only ever sees ciphertext, so http on a private network
// exposes metadata to that network at most.
func Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil {
		return errors.New("invalid relay URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if PrivateHost(u.Hostname()) {
			return nil
		}
		return errors.New("invalid relay URL: plain http is only allowed for localhost or a private network address")
	}
	return errors.New("invalid relay URL: must be https")
}

// PrivateHost reports whether host names this machine or a private network:
// localhost, a loopback, private or CGNAT address, or a single-label ".local"
// name.
func PrivateHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" {
		return true
	}
	if strings.HasSuffix(h, ".local") && len(h) > len(".local") && !strings.Contains(strings.TrimSuffix(h, ".local"), ".") {
		return true
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || cgnat.Contains(addr)
}
