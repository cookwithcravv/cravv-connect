package daemon

import (
	"os"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/fakeagent"
)

// TestMain lets this test binary run as the fake agent (the SessionHost
// tests start it as their agent program).
func TestMain(m *testing.M) {
	fakeagent.Main()
	os.Exit(m.Run())
}
