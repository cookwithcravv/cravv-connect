package cli

import (
	"runtime"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	defer func(v string) { Version = v }(Version)
	fd := newFakeDaemon(t) // never started: version needs no daemon
	Version = "v1.2.3"
	want := "cravv-connect v1.2.3 (" + runtime.GOOS + "/" + runtime.GOARCH + ")\n"
	if r := fd.run(nil, "version"); r.code != 0 || r.stdout != want {
		t.Fatalf("code %d stdout %q, want %q", r.code, r.stdout, want)
	}
	// A test binary has no module version: an unstamped build says dev.
	Version = "dev"
	if got := BuildVersion(); got != "dev" {
		t.Fatalf("unstamped build reports %q", got)
	}
}
