package daemon

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// cgnat is the carrier-grade NAT range that Tailscale uses for node addresses.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// validRelayURL accepts a relay URL learned from a peer (pairing payload or
// control.relay_moved): https with a host, or plain http only for relays on
// this machine or a private network (loopback, RFC 1918 and unique local
// addresses, Tailscale's 100.64.0.0/10, and mDNS ".local" names). The relay
// only ever sees ciphertext, so http on a private network exposes metadata to
// that network at most.
func validRelayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil {
		return errors.New("invalid relay URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if privateHost(u.Hostname()) {
			return nil
		}
		return errors.New("invalid relay URL: plain http is only allowed for localhost or a private network address")
	}
	return errors.New("invalid relay URL: must be https")
}

func privateHost(host string) bool {
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
