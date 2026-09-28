// Command cravv-conformance runs the relay-v1 conformance suite against a live relay:
//
//	CRAVV_CONFORMANCE_ADMIN_TOKEN=<t> go run ./cmd/cravv-conformance --relay http://127.0.0.1:8787
//
// The admin token comes from --admin-token, where "-" reads it from the first line of
// stdin, or else from CRAVV_CONFORMANCE_ADMIN_TOKEN (or the older CRAVV_RELAY_ADMIN_TOKEN).
// Prefer the environment or stdin: a flag value is visible to other users in ps output.
// TTL cases are skipped (an external relay's clock cannot be advanced). Pass --slow
// (or set CRAVV_CONFORMANCE_SLOW=1) to also fill a mailbox queue to its caps.
// Standard test flags work too, for example -test.run 'Conformance/rooms'.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/conformance"
	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/transport"
)

type options struct {
	relay      string
	adminToken string
	slow       bool
}

// Environment variables that may hold the admin token, in order of preference.
const (
	tokenEnv       = "CRAVV_CONFORMANCE_ADMIN_TOKEN"
	legacyTokenEnv = "CRAVV_RELAY_ADMIN_TOKEN"
)

func parseOptions(fs *flag.FlagSet, args []string, getenv func(string) string, stdin io.Reader) (options, error) {
	var o options
	fs.StringVar(&o.relay, "relay", "", "relay URL, scheme://host[:port] (required)")
	fs.StringVar(&o.adminToken, "admin-token", "", `relay admin token; "-" reads it from stdin (or env `+tokenEnv+`)`)
	fs.BoolVar(&o.slow, "slow", false, "also run the queue-cap cases (10000 frames)")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if o.adminToken == "-" {
		tok, err := readToken(stdin)
		if err != nil {
			return options{}, fmt.Errorf("reading the admin token from stdin: %w", err)
		}
		if tok == "" {
			return options{}, fmt.Errorf("--admin-token -: stdin has no token")
		}
		o.adminToken = tok
	}
	if o.adminToken == "" {
		o.adminToken = getenv(tokenEnv)
	}
	if o.adminToken == "" {
		o.adminToken = getenv(legacyTokenEnv)
	}
	if o.relay == "" {
		return options{}, fmt.Errorf("--relay is required")
	}
	if o.adminToken == "" {
		return options{}, fmt.Errorf("an admin token is required: %s, or --admin-token (\"-\" for stdin)", tokenEnv)
	}
	return o, nil
}

// readToken returns the first line of r without surrounding whitespace.
func readToken(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func newIdentity() transport.Signer {
	id, err := keys.GenerateIdentity()
	if err != nil {
		panic(err)
	}
	return id
}

func main() {
	testing.Init()
	o, err := parseOptions(flag.CommandLine, os.Args[1:], os.Getenv, os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cravv-conformance:", err)
		os.Exit(2)
	}
	if o.slow {
		_ = os.Setenv(conformance.SlowEnv, "1")
	}
	if f := flag.Lookup("test.v"); f != nil && f.Value.String() == "false" {
		_ = flag.Set("test.v", "true")
	}
	target := conformance.Target{URL: o.relay, AdminToken: o.adminToken, NewIdentity: newIdentity}
	tests := []testing.InternalTest{{Name: "Conformance", F: func(t *testing.T) { conformance.Run(t, target) }}}
	testing.Main(regexp.MatchString, tests, nil, nil)
}
