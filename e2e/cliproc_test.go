package e2e

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/cli"
	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/fakeagent"
)

// envAsCLI makes this test binary run as the cravv-connect command line, so
// a test can start a real cravv-connect process (as an agent does with the
// listener) without building the binary.
const envAsCLI = "CRAVV_E2E_AS_CLI"

// cliProcessMain runs the command line and exits when envAsCLI is set;
// otherwise it returns at once.
func cliProcessMain() {
	if os.Getenv(envAsCLI) != "1" {
		return
	}
	os.Exit(cli.Main(os.Args[1:], cli.DefaultEnv()))
}

// ProcessCLI starts `cravv-connect args...` as its own process against node
// n's daemon ($CRAVV_HOME is the node's state folder) and returns its
// result channel. A process still running when the test ends is killed.
func (n *Node) ProcessCLI(args ...string) <-chan CLIRun {
	n.t.Helper()
	// The command line finds the daemon at $CRAVV_HOME/daemon.sock.
	if err := os.Symlink(n.Paths.Socket, filepath.Join(n.Dir, "daemon.sock")); err != nil && !errors.Is(err, os.ErrExist) {
		n.t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		n.t.Fatal(err)
	}
	cmd := exec.Command(self, args...)
	cmd.Dir = n.Proj
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, fakeagent.EnvMode+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, envAsCLI+"=1", config.EnvHome+"="+n.Dir)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Start(); err != nil {
		n.t.Fatal(err)
	}
	done := make(chan CLIRun, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		err := cmd.Wait()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			code = -1
		}
		done <- CLIRun{Code: code, Stdout: out.String(), Stderr: errb.String()}
	}()
	n.t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-exited
	})
	return done
}

// ListenerProcess starts the listener command a session_share result gave
// the agent (`cravv-connect listen --wake-file <path>`) as a background
// process, the way Claude Code runs it.
func (n *Node) ListenerProcess(t *testing.T, listener string) <-chan CLIRun {
	t.Helper()
	file, ok := strings.CutPrefix(listener, "cravv-connect listen --wake-file ")
	if !ok {
		t.Fatalf("listener command %q", listener)
	}
	return n.ProcessCLI("listen", "--wake-file", file)
}
