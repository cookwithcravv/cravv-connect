package e2e

import (
	"os"
	"testing"

	"github.com/cravv/cravv-connect/internal/fakeagent"
)

// TestMain lets this test binary run as the fake claude of managed runs
// (the managed tests point CRAVV_CLAUDE at it).
func TestMain(m *testing.M) {
	fakeagent.Main()
	os.Exit(m.Run())
}
