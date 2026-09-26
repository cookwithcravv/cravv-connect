package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/install"
	"github.com/cravv/cravv-connect/internal/ipc"
)

type fakeInstaller struct {
	name      string
	detected  bool
	installed string
	removed   bool
	err       error
}

func (f *fakeInstaller) Name() string { return f.name }
func (f *fakeInstaller) Detect() bool { return f.detected }
func (f *fakeInstaller) Install(_ context.Context, bin string) error {
	f.installed = bin
	return f.err
}
func (f *fakeInstaller) Uninstall(context.Context) error { f.removed = true; return nil }

type fakeSetup struct{ installed, removed string }

func (f *fakeSetup) Install(_ context.Context, bin string) error { f.installed = bin; return nil }
func (f *fakeSetup) Uninstall(context.Context) error             { f.removed = "yes"; return nil }

func TestInstallCommands(t *testing.T) {
	fd := newFakeDaemon(t)
	claude := &fakeInstaller{name: "claude", detected: true}
	codex := &fakeInstaller{name: "codex"}
	env, out, errb := fd.env(&fakePrompter{}, "")
	env.Agents = install.NewRegistry(claude, codex)
	env.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }

	if code := Main([]string{"install"}, env); code != 0 {
		t.Fatal(errb.String())
	}
	want := "AGENT   STATUS\nclaude  detected\ncodex   not found\nRun `cravv-connect install <agent>`. For Cursor, VS Code and Gemini CLI, see docs/agents.md.\n"
	if out.String() != want {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if code := Main([]string{"install", "claude"}, env); code != 0 || claude.installed != "/usr/local/bin/cravv-connect" {
		t.Fatalf("%d %q", code, errb.String())
	}
	if out.String() != "Installed cravv-connect for claude. Restart the agent so it loads the MCP server.\n" {
		t.Fatalf("%q", out.String())
	}
	errb.Reset()
	if code := Main([]string{"install", "cursor"}, env); code != 1 || !strings.Contains(errb.String(), `unknown agent "cursor" (supported: claude, codex)`) {
		t.Fatalf("%q", errb.String())
	}
	out.Reset()
	if code := Main([]string{"uninstall", "codex"}, env); code != 0 || !codex.removed || out.String() != "Removed cravv-connect from codex.\n" {
		t.Fatalf("%q", out.String())
	}
	codex.err = errors.New("boom")
	errb.Reset()
	if code := Main([]string{"install", "codex"}, env); code != 1 || errb.String() != "error: boom\n" {
		t.Fatalf("%q", errb.String())
	}
}

func TestDaemonInstallCommands(t *testing.T) {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{})
	fd.start()
	env, out, errb := fd.env(&fakePrompter{}, "")
	env.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }
	if code := Main([]string{"daemon", "install"}, env); code != 1 || !strings.Contains(errb.String(), "no supported service manager") {
		t.Fatalf("no manager: %q", errb.String())
	}
	setup := &fakeSetup{}
	env.ServiceSetup = setup
	if code := Main([]string{"daemon", "install"}, env); code != 0 || setup.installed != "/usr/local/bin/cravv-connect" ||
		!strings.HasPrefix(out.String(), "Daemon installed as a login service and started.\n") {
		t.Fatalf("%q", out.String())
	}
	out.Reset()
	if code := Main([]string{"daemon", "uninstall"}, env); code != 0 || setup.removed == "" || out.String() != "Daemon service removed.\n" {
		t.Fatalf("%q", out.String())
	}
}

// `daemon install` from `go run` or a test binary would point the login
// service at a file that is deleted soon: refuse it.
func TestDaemonInstallRefusesTemporaryBinary(t *testing.T) {
	fd := newFakeDaemon(t)
	for _, bin := range []string{
		"/Users/me/Library/Caches/go-build/ab/cd/exe/cravv-connect",
		"/private/var/folders/x/T/go-build1234/b001/exe/cravv-connect",
		filepath.Join(os.TempDir(), "cravv-build", "cravv-connect"),
	} {
		env, _, errb := fd.env(&fakePrompter{}, "")
		setup := &fakeSetup{}
		env.ServiceSetup = setup
		env.Executable = func() (string, error) { return bin, nil }
		if code := Main([]string{"daemon", "install"}, env); code != 1 || setup.installed != "" ||
			!strings.Contains(errb.String(), "make build then bin/cravv-connect daemon install") {
			t.Fatalf("%s: code %d installed %q stderr %q", bin, code, setup.installed, errb.String())
		}
	}
}

// A service that does not come up is reported, with where to look.
func TestDaemonInstallReportsDaemonNotStarting(t *testing.T) {
	old := installWait
	installWait = 200 * time.Millisecond
	t.Cleanup(func() { installWait = old })
	fd := newFakeDaemon(t) // never started: nothing answers on the socket
	env, _, errb := fd.env(&fakePrompter{}, "")
	env.ServiceSetup = &fakeSetup{}
	env.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }
	if code := Main([]string{"daemon", "install"}, env); code != 1 ||
		!strings.Contains(errb.String(), "did not answer") || !strings.Contains(errb.String(), "daemon.log") {
		t.Fatalf("code %d stderr %q", code, errb.String())
	}
}
