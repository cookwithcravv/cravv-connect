// Command cravv-conformance runs the relay-v1 conformance suite against a live relay:
//
//	go run ./cmd/cravv-conformance --relay http://127.0.0.1:8787 --admin-token <t>
//
// TTL cases are skipped (an external relay's clock cannot be advanced). Pass --slow
// (or set CRAVV_CONFORMANCE_SLOW=1) to also fill a mailbox queue to its caps.
// Standard test flags work too, for example -test.run 'Conformance/rooms'.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/cravv/cravv-connect/conformance"
	"github.com/cravv/cravv-connect/internal/keys"
	"github.com/cravv/cravv-connect/internal/transport"
)

type options struct {
	relay      string
	adminToken string
	slow       bool
}

func parseOptions(fs *flag.FlagSet, args []string, getenv func(string) string) (options, error) {
	var o options
	fs.StringVar(&o.relay, "relay", "", "relay URL, scheme://host[:port] (required)")
	fs.StringVar(&o.adminToken, "admin-token", "", "relay admin token (or env CRAVV_RELAY_ADMIN_TOKEN)")
	fs.BoolVar(&o.slow, "slow", false, "also run the queue-cap cases (10000 frames)")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if o.adminToken == "" {
		o.adminToken = getenv("CRAVV_RELAY_ADMIN_TOKEN")
	}
	if o.relay == "" {
		return options{}, fmt.Errorf("--relay is required")
	}
	if o.adminToken == "" {
		return options{}, fmt.Errorf("an admin token is required: --admin-token or CRAVV_RELAY_ADMIN_TOKEN")
	}
	return o, nil
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
	o, err := parseOptions(flag.CommandLine, os.Args[1:], os.Getenv)
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
