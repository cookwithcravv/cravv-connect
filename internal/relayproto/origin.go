package relayproto

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ErrBadOrigin is returned by NormalizeOrigin for anything that is not an http(s) origin.
var ErrBadOrigin = errors.New("relayproto: invalid origin")

// NormalizeOrigin returns the canonical relay-v1 origin for s: scheme://host[:port]
// with a lowercase scheme and host, the default port (443 for https, 80 for http)
// omitted, no trailing dot on the host, IPv6 literals in brackets, and no path, query,
// fragment, or user info. A lone trailing "/" is accepted and dropped.
func NormalizeOrigin(s string) (string, error) {
	bad := func(why string) (string, error) { return "", fmt.Errorf("%w %q: %s", ErrBadOrigin, s, why) }
	u, err := url.Parse(s)
	if err != nil {
		return bad(err.Error())
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return bad("scheme must be http or https")
	}
	if u.Opaque != "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return bad("use scheme://host[:port] only")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return bad("missing host")
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return bad("bad IPv6 literal")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return bad("bad port")
		}
		port = strconv.Itoa(n)
		if (scheme == "https" && n == 443) || (scheme == "http" && n == 80) {
			port = ""
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, nil
}
