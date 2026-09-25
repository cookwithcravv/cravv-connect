package daemon

import (
	"errors"
	"net/url"
)

// validRelayURL accepts a relay URL learned from a peer (pairing payload or
// control.relay_moved): https with a host, or plain http only for loopback
// development relays (localhost, 127.0.0.1, ::1).
func validRelayURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil {
		return errors.New("invalid relay URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return nil
		}
		return errors.New("invalid relay URL: plain http is only allowed for localhost")
	}
	return errors.New("invalid relay URL: must be https")
}
