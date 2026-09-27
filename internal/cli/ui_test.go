package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// fakeBrowser replaces openBrowser for one test.
func fakeBrowser(t *testing.T, err error) *[]string {
	t.Helper()
	var opened []string
	old := openBrowser
	openBrowser = func(url string) error {
		opened = append(opened, url)
		return err
	}
	t.Cleanup(func() { openBrowser = old })
	return &opened
}

const launchURL = "http://127.0.0.1:4242/launch?token=abc"

func TestUIOpensTheBrowser(t *testing.T) {
	opened := fakeBrowser(t, nil)
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodUIStart, ipc.GateAllowWhenKilled, ipc.UIStartResult{URL: launchURL})
	fd.start()
	r := fd.run(nil, "ui")
	want := "cravv-connect UI:\n  " + launchURL + "\nThe link works once, within 2 minutes. Run cravv-connect ui again for a new one.\n"
	if r.code != 0 || r.stdout != want {
		t.Fatalf("code %d\n%s", r.code, r.stdout)
	}
	if len(*opened) != 1 || (*opened)[0] != launchURL {
		t.Fatalf("opened %v", *opened)
	}
}

func TestUINoBrowser(t *testing.T) {
	opened := fakeBrowser(t, nil)
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodUIStart, ipc.GateAllowWhenKilled, ipc.UIStartResult{URL: launchURL})
	fd.start()
	if r := fd.run(nil, "ui", "--no-browser"); r.code != 0 || !strings.Contains(r.stdout, launchURL) || len(*opened) != 0 {
		t.Fatalf("code %d, opened %v\n%s", r.code, *opened, r.stdout)
	}
}

// A browser that cannot be opened is not an error: the link is printed.
func TestUIBrowserFailureStillPrintsTheLink(t *testing.T) {
	fakeBrowser(t, errors.New("no display"))
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodUIStart, ipc.GateAllowWhenKilled, ipc.UIStartResult{URL: launchURL})
	fd.start()
	r := fd.run(nil, "ui")
	if r.code != 0 || !strings.Contains(r.stdout, launchURL) || r.stderr != "Could not open a browser (no display). Open the link above yourself.\n" {
		t.Fatalf("code %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
}
