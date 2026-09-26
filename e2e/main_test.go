package e2e

import (
	"os"
	"testing"

	"github.com/cravv/cravv-connect/internal/fakeagent"
)

// TestMain lets this test binary run as the fake claude of managed runs
// (the managed tests point CRAVV_CLAUDE at it) and as the cravv-connect
// command line (ProcessCLI starts it that way).
func TestMain(m *testing.M) {
	fakeagent.Main()
	cliProcessMain()
	os.Exit(m.Run())
}
