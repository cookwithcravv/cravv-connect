package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// buildBinary builds cravv-connect into a temporary folder.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cravv-connect")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/cookwithcravv/cravv-connect/cmd/cravv-connect").CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// Review focus: `cravv-connect mcp` stopped with SIGTERM or SIGINT (an
// agent quitting) still removes the chat's wake file on its way out.
func TestMCPRemovesTheWakeFileOnSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := buildBinary(t)
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			n := NewNode(t, NewRelay(t), "alice", NodeOptions{})
			// The binary finds the daemon at $CRAVV_HOME/daemon.sock.
			if err := os.Symlink(n.Paths.Socket, filepath.Join(n.Dir, "daemon.sock")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "mcp", "--project-dir", n.Proj)
			cmd.Env = append(os.Environ(), config.EnvHome+"="+n.Dir, "CLAUDE_CODE_SESSION_ID=chat-sig")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "1"}, nil)
			cs, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "session_share", Arguments: map[string]any{"name": "lead"}})
			if err != nil || res.IsError {
				t.Fatalf("session_share: %+v, %v", res, err)
			}
			var out struct {
				Listener string `json:"listener"`
			}
			if err := json.Unmarshal([]byte(res.Content[0].(*mcp.TextContent).Text), &out); err != nil {
				t.Fatal(err)
			}
			_, file, ok := strings.Cut(out.Listener, " listen --wake-file ")
			if !ok {
				t.Fatalf("listener %q", out.Listener)
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatalf("no wake file after share: %v", err)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() { cs.Wait(); close(exited) }()
			Eventually(t, 10*time.Second, "the wake file is removed", func() bool {
				_, err := os.Stat(file)
				return os.IsNotExist(err)
			})
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatal("cravv-connect mcp did not exit")
			}
		})
	}
}
