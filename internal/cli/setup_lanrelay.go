package cli

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// lanRelayPort is the port of the LAN test relay setup starts.
const lanRelayPort = 8787

// relayStartWait is how long setup waits for the LAN test relay to answer.
var relayStartWait = 5 * time.Second

// hostLabel is a host name label that can be looked up as <label>.local.
var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// lanHost names this machine for other machines on the network: <host>.local
// when it resolves (it survives DHCP address changes), else the first private
// address, else 127.0.0.1 (this machine only).
func (s *setup) lanHost() string {
	if h, err := s.env.Hostname(); err == nil {
		label, _, _ := strings.Cut(h, ".")
		if hostLabel.MatchString(label) {
			name := strings.ToLower(label) + ".local"
			if addrs, err := s.sys.LookupHost(s.ctx, name); err == nil && len(addrs) > 0 {
				return name
			}
		}
	}
	if addrs, err := s.sys.PrivateAddrs(); err == nil {
		for _, a := range addrs {
			if a.Is4() {
				return a.String()
			}
		}
		if len(addrs) > 0 {
			return addrs[0].String()
		}
	}
	return "127.0.0.1"
}

// startLANRelay starts the Go reference relay (cravv-relay, next to this
// binary) on every interface, with a fresh admin token passed in its
// environment (never on its command line), and waits until it answers. It
// returns the relay origin and the admin token.
func (s *setup) startLANRelay() (string, string, error) {
	exe, err := s.env.Executable()
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	bin := filepath.Join(filepath.Dir(exe), "cravv-relay")
	host := s.lanHost()
	origin := "http://" + net.JoinHostPort(host, strconv.Itoa(lanRelayPort))
	if s.sys.RelayHealthy(s.ctx, origin) == nil {
		return "", "", fmt.Errorf("a relay already answers at %s. If another machine is on it, set this one up with `cravv-connect setup --join <code>` "+
			"(a join code from `cravv-connect pair` there); if an earlier setup started it, stop it (pkill -x cravv-relay) and run setup again", origin)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := os.MkdirAll(s.paths.Home, 0o700); err != nil {
		return "", "", err
	}
	logPath := filepath.Join(s.paths.Home, "relay.log")
	args := []string{"-addr", net.JoinHostPort("0.0.0.0", strconv.Itoa(lanRelayPort)), "-origin", origin}
	if err := s.sys.StartRelay(bin, args, []string{"CRAVV_RELAY_ADMIN_TOKEN=" + token}, logPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("cravv-relay was not found next to cravv-connect (%s); install it from the release archive, or enter a relay URL", bin)
		}
		return "", "", err
	}
	deadline := time.Now().Add(relayStartWait)
	for {
		err := s.sys.RelayHealthy(s.ctx, origin)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("the LAN test relay did not answer at %s within %s: %w (see %s)", origin, relayStartWait, err, logPath)
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(s.w, "Started a LAN test relay at %s (log: %s).\n", origin, logPath)
	fmt.Fprintln(s.w, "It keeps everything in memory and stops when this machine restarts; for real use, deploy the Cloudflare relay.")
	if host == "127.0.0.1" {
		fmt.Fprintln(s.w, "No network address was found, so only this machine can reach it.")
	}
	return origin, token, nil
}
