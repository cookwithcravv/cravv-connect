package cli

import (
	"strings"
	"testing"
)

// files accept is gone: v2 never holds a file for a human (a link's
// permission covers files), and the upgrade declined the files older
// versions held. files only lists.
func TestFilesAcceptIsRetired(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.start()
	r := fd.run(nil, "files", "--help")
	if strings.Contains(r.stdout, "accept") {
		t.Fatalf("files help still offers accept:\n%s", r.stdout)
	}
	if r := fd.run(nil, "files", "accept", "F1"); r.code == 0 || len(fd.methods()) != 0 {
		t.Fatalf("files accept: %d %q %q, calls %v", r.code, r.stdout, r.stderr, fd.methods())
	}
}
