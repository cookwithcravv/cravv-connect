package cli

import (
	"strings"
	"testing"
)

// files accept describes what v2 still holds: only files held before the
// upgrade (no link level holds files now).
func TestFilesAcceptHelpMatchesV2(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.start()
	r := fd.run(nil, "files", "--help")
	if strings.Contains(r.stdout, "chat-only") || !strings.Contains(r.stdout, "before the upgrade to v2") {
		t.Fatalf("files help:\n%s", r.stdout)
	}
}
