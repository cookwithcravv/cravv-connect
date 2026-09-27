package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/install"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
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

// fakeOptInstaller takes install options, as the Claude Code installer does.
type fakeOptInstaller struct {
	fakeInstaller
	opts []install.Options
}

func (f *fakeOptInstaller) InstallWith(_ context.Context, bin string, o install.Options) error {
	f.installed = bin
	f.opts = append(f.opts, o)
	return nil
}

func TestInstallAllowSend(t *testing.T) {
	fd := newFakeDaemon(t)
	claude := &fakeOptInstaller{fakeInstaller: fakeInstaller{name: "claude", detected: true}}
	codex := &fakeInstaller{name: "codex"}
	env, out, errb := fd.env(&fakePrompter{}, "")
	env.Agents = install.NewRegistry(claude, codex)
	env.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }
	if code := Main([]string{"install", "claude"}, env); code != 0 || !strings.Contains(out.String(), "--allow-send") || !strings.Contains(out.String(), "--no-allow-send") {
		t.Fatalf("%d %q %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := Main([]string{"install", "claude", "--allow-send"}, env); code != 0 || strings.Contains(out.String(), "--no-allow-send") {
		t.Fatalf("%d %q", code, out.String())
	}
	if code := Main([]string{"install", "claude", "--no-allow-send"}, env); code != 0 {
		t.Fatalf("%d %q", code, errb.String())
	}
	if len(claude.opts) != 3 || claude.opts[0] != (install.Options{}) || claude.opts[1] != (install.Options{AllowSend: true}) || claude.opts[2] != (install.Options{NoAllowSend: true}) {
		t.Fatalf("options %+v", claude.opts)
	}
	if code := Main([]string{"install", "claude", "--allow-send", "--no-allow-send"}, env); code != 1 || len(claude.opts) != 3 {
		t.Fatalf("both flags: %d %q", code, errb.String())
	}
	if code := Main([]string{"install", "codex", "--no-allow-send"}, env); code != 1 || codex.installed != "" {
		t.Fatalf("%d %q", code, errb.String())
	}
	if code := Main([]string{"install", "codex", "--allow-send"}, env); code != 1 || !strings.Contains(errb.String(), "apply to Claude Code only") || codex.installed != "" {
		t.Fatalf("%d %q", code, errb.String())
	}
}

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
