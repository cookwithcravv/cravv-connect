# cravv-connect v2 Phase 6: E2E and Docs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove v2 end to end and document it. An acceptance suite in `e2e/` has one test per success criterion of spec section 2 (`TestAcceptance_<N>_<Name>`, each quoting its criterion), against real daemons on an in-process relay and through the surfaces people and agents use: the real MCP server with an elicitation-capable client, the listener as a real process, the CLI, `setup` and `setup --join` on two fresh machines, and the web UI over HTTP. A named test runs the user's own setup (two linked chats on each of a Mac and a GPU box). README, `docs/security.md`, `docs/agents.md`, `protocol/peer-v1.md` and `protocol/ipc-v1.md` are rewritten for v2 from the code, and tests keep them honest: the IPC method table is generated from the daemon's registry, the README's CLI reference must name every command and flag, and no Markdown file may contain an em dash.

**Architecture:**
- New code is test code in `e2e/` plus documents. Production code is not touched, so the work merges cleanly with the Phase 3 review fixes on `main`.
- `e2e/cliproc_test.go`: `TestMain` lets the test binary run as `cravv-connect` (`CRAVV_E2E_AS_CLI=1`), the same trick `internal/fakeagent` uses for `claude`, so `Node.ProcessCLI` and `Node.ListenerProcess` start real processes without a build step.
- `e2e/harness.go` gains `NewLANRelay` (a relay whose URL is `http://cravv-relay.local:<port>`) and `Relay.HTTPClient` (dials the real address), because setup treats a loopback relay as unreachable from other machines and join codes refuse public plain http.
- `e2e/acceptance_setup_test.go`: `freshMachine` drives the real `cli.Main` with the seams setup already has (`Prompter`, `SetupSystem`, `ServiceManager`/`ServiceInstaller`, `install.Registry`); its "login service" starts the real daemon in this process from what setup wrote.
- `e2e/acceptance_test.go` (criteria 1 to 3), `acceptance_remote_test.go` (4, 5), `acceptance_setup_test.go` (6), `acceptance_control_test.go` (7, 8) and `scenario_test.go` reuse the existing helpers (`NewPair`, `LinkUp`, `newClaudeAgent`, `Human`, `newManagedPair`, `OpenUI`, `recordingBackend`).
- `e2e/docs_ipc_test.go` and `e2e/docs_test.go` are the drift guards.

**Tech Stack:** Go 1.26 standard library (`go/ast`, `go/parser`, `os/exec`, `net/http`, `regexp`), `github.com/modelcontextprotocol/go-sdk` (in-memory MCP client with an elicitation handler), `github.com/spf13/cobra` and `github.com/spf13/pflag` (the command tree), the existing e2e harness. No new modules.

**Spec:** `docs/superpowers/specs/2026-09-26-cravv-connect-v2-sessions-design.md`, all of it; section 2 (success criteria) is the test target, section 11 item 6 this phase. Earlier plans: `docs/superpowers/plans/2026-09-26-cravv-connect-v2-phase1-links.md` to `-phase5-web-ui.md` (their Decisions and "What later phases need" sections feed the docs).

**Verified:** every task below was implemented in a scratch worktree on top of `main` at `e2f1739` (Phases 1 to 5 with their review fixes, including the Phase 3 security review's managed-run containment), one commit per task. After each commit `gofmt -l internal e2e cmd scripts` printed nothing, `go vet ./...` was clean and `go test ./... -race -count=1` passed (36 packages, e2e included). The file contents and the patch below are those commits, byte for byte; this plan was generated from them, then replayed onto a fresh worktree at `e2f1739`, and every task's tree compared equal to its commit. `go test ./e2e -race -count=2` takes about 42 seconds on an Apple Silicon Mac.

## Global Constraints

- Everything in the Phase 1 plan's Global Constraints still holds (module path, `go 1.26`, cgo only in `internal/auth`, `core.Clock` for time, `crypto/rand` only, no em dashes in user-facing text).
- After every task: `gofmt -l internal e2e cmd scripts` prints nothing, `go vet ./...` is clean, `go test ./... -race -count=1` passes (e2e included).
- **Merge safety.** Only `e2e/` and documents change: `e2e/main_test.go` and `e2e/harness.go` are the only existing Go files edited, and `README.md`, `docs/security.md`, `docs/agents.md`, `protocol/peer-v1.md` and `protocol/ipc-v1.md` the only documents. No production package is touched.
- **Test hygiene.** Acceptance tests use `t.Parallel()` except where they set the environment (`TestAcceptance_4_ExistingOrNew`, like the other managed tests). State folders are short temp folders removed at cleanup (unix socket paths are limited to about 104 bytes); child processes are killed at cleanup. Disk use is bounded: no binary is built.
- **Real paths, fake edges.** Only the seams the e2e harness already uses differ from production: `auth.Fake` for PAM, the identity in the settings table instead of the Keychain, a recording desktop notifier, a `core.FakeClock` where 150 seconds must pass, the fake agent as `claude`, and for setup the service manager, the `claude` CLI runner and the health check's HTTP client.
- **Docs follow the code.** Every statement in the rewritten documents was checked against the code at `e2f1739`; where the spec and the code disagree, the documents say what the code does (see Decisions).
- **Copy.** No em dashes in any Markdown file (`TestDocsHaveNoEmDashes`), including this plan.

## Review Focus

These failure modes follow from the spec but no single happy path exercises them. Each is pinned by the named tests.

1. **An idle chat that is not woken, or a wake-up that leaks.** The listener must be a real background process that exits with exactly one line naming only the local alias and link number, for a link request, a message and a held task, and never print peer text; the wake token must reach it through a `0600` file named on its command line, never the token itself. Tests: `TestAcceptance_1_ChatIsTheHub`, `TestAcceptance_8_SecurityHolds/session_identity_cannot_be_taken_over`.
2. **Cross-talk between two sessions on one machine.** With two sessions per machine, nothing on one link may reach another session's inbox, tasks or model, and another session cannot act on the link's number or task IDs. Tests: `TestAcceptance_2_SessionLinks`, `TestScenario_MacAndGPUBox` (records every tool result each of four models saw).
3. **Presence that closes too early or never.** A machine that drops must close its links on both sides after 150 seconds, and a session away briefly (an MCP reconnect, or a relay outage shorter than the timeout) must keep them; the second subtest is shown to fail when the pongs after the outage are removed. Test: `TestAcceptance_3_Presence`.
4. **Setup that stalls, asks the wrong things or pairs through the wrong relay.** Two fresh machines must reach paired and chat-connected with `setup` and `setup --join` alone, answering exactly the expected questions, against a relay URL that join codes accept. Test: `TestAcceptance_6_Setup`.
5. **Documents that drift from the code.** The IPC method table must equal the daemon's registry (gates, while killed, from a run), every method must be documented and none invented, the README's CLI reference must name every command and flag of the real command tree and nothing else, and no Markdown file may contain an em dash. Tests: `TestIPCDocMatchesRegistry`, `TestREADMEDocumentsEveryCommand`, `TestDocsHaveNoEmDashes`.

## Decisions (verified while building)

- **The test binary is the `cravv-connect` process.** `TestMain` calls `cliProcessMain()`, which runs `cli.Main(os.Args[1:], cli.DefaultEnv())` when `CRAVV_E2E_AS_CLI=1`. The listener therefore runs as a separate process with its own stdin, exit code and `$CRAVV_HOME`, as Claude Code runs it, and no binary is built (disk space and time). The node's socket is linked as `$CRAVV_HOME/daemon.sock`, where the command line looks for it.
- **Presence on a fake clock.** Both daemons share one `core.FakeClock`; the relay is stopped before the first tick, so no presence frame from before the drop can arrive late and refresh a link. For the brief absence, the Mac's ping and the GPU box's pong are ordered with chat messages sent after them (one mailbox, in order), which is how the test knows fresh evidence arrived before it advances the clock again.
- **A LAN-named relay for setup.** The relay's public origin is `http://cravv-relay.local:<port>`: setup does not treat it as this-machine-only (so it offers pairing), and `relayaddr.Check` accepts it in a join code. Nothing resolves the name: the daemons get `relayclient.New(origin, relayclient.WithHTTPClient(relay.HTTPClient()))`, whose dialer goes to the relay's real address, and signatures still cover the configured origin.
- **Setup's login service starts the real daemon in the test process** from `config.toml` and `store.db` as setup wrote them (including the admin token `init` stored), with the same options as `NewNode`. The installed binary path is a fixed path outside the temporary folder, because `daemon install` refuses temporary builds.
- **Criterion 7 from the chat.** From the chat an agent sees devices (`machines`), sessions and offers (`sessions`) and changes links (`connect`, `restrict`, `disconnect`). Changing a device (pause, unpair) or an offer rule is for the human (spec 7.3 and 10), so the test changes those on the web page and checks that the chat sees the change.
- **`protocol/peer-v1.md` is updated in place, not renamed.** The frame, the envelope (`"v": 1`) and the sealing label `cravv-connect/peer-v1` are unchanged; version 2 is the protocol version inside (`core.ProtocolVersion`, `control.unsupported{min_version: 2}`). A `peer-v2.md` would suggest a new wire format. The document says so at the top.
- **The IPC method table is generated.** `TestIPCDocMatchesRegistry` renders it from `ipc.Server.Methods()` on the registries `app.Serve` uses and from `ipc.RunMethods`, and rewrites it with `CRAVV_UPDATE_DOCS=1`. Parsing the `Method*` constants catches a method registered outside those registries. The per-method params and notes stay hand-written, but every registered method must have a row and every row a method.
- **Rebased on the Phase 3 review.** `main` moved to `e2f1739` while this phase was built (managed-run containment, a run cannot read the inbox, run tokens bind one connection, `tasks-ask` to a managed session becomes `messages`). The acceptance tests passed unchanged; the IPC table (runs lost `inbox.check` and `inbox.wait`), the peer protocol's offer rule and the security document's managed-sessions section (merged with the one the review added) follow the new code.
- **Where the documents say what the code does, not what the spec says:**
  - The wake token reaches the listener in a `0600` file (`--wake-file`), not on stdin (spec 7.1; Phase 2 decision: a command text is visible in `ps` and the transcript). `listen` still reads stdin when no file is given.
  - The v1 `--json` agent commands are gone and their link-scoped replacement is not built (spec 3.1 says they were replaced; Phase 2 deferred it). `docs/agents.md` says so and points to ipc-v1 and `listen`.
  - The web UI's Devices page shows the bind code as text; join codes and QR codes are in the CLI only (spec 9 lists them for the page).
  - A managed run's prompt goes on stdin, not in argv (spec 6.2 writes `claude -p <prompt>`; Phase 3 decision).
  - Receivers also cap link requests at 10 per peer per minute (`core.LinkRequestsPerMinute`), beyond spec 4's 5 pending.
  - Disconnect and restrict need no password, as spec 10 says, but the CLI has no command for them: they are in the chat's tools and on the web UI's Sessions page. `cravv-connect link permit` always asks for the password (its IPC gate is `password`), although its help says "raising asks for your password".
  - `cravv-connect files accept` still says "a held file from a chat-only peer": links never hold files in v2, so it only applies to files held before the upgrade (the README and ipc-v1 say so).
  - An agent connection calling an owner's method (`offers.*`, `managed.*`, `ui.start`, `sessions.local`, `link.connect_as`) gets `bad_request`, not `not_permitted`.

## File Structure

| File | Change | Responsibility |
|---|---|---|
| `e2e/main_test.go` | Modify | `TestMain` also runs `cliProcessMain`. |
| `e2e/cliproc_test.go` | Create | The test binary as the `cravv-connect` command line; `Node.ProcessCLI`, `Node.ListenerProcess`. |
| `e2e/acceptance_test.go` | Create | Criteria 1 (chat hub), 2 (session links), 3 (presence); `shareChat`, `taskState`. |
| `e2e/acceptance_remote_test.go` | Create | Criteria 4 (existing or new) and 5 (one approval). |
| `e2e/harness.go` | Modify | `NewLANRelay`, `LANRelayHost`, `Relay.HTTPClient`, a relay origin that differs from its address. |
| `e2e/acceptance_setup_test.go` | Create | Criterion 6: `freshMachine` and the setup rig. |
| `e2e/acceptance_control_test.go` | Create | Criteria 7 (UI) and 8 (security); `offerFolder`. |
| `e2e/scenario_test.go` | Create | `TestScenario_MacAndGPUBox`. |
| `e2e/docs_ipc_test.go` | Create | `TestIPCDocMatchesRegistry`: the generated method table and the method reference. |
| `e2e/docs_test.go` | Create | `TestREADMEDocumentsEveryCommand`, `TestDocsHaveNoEmDashes`. |
| `protocol/ipc-v1.md` | Rewrite | The local API for v2. |
| `protocol/peer-v1.md` | Modify | Protocol version 2 on the peer-v1 wire format. |
| `docs/security.md` | Rewrite | The v2 threat model. |
| `docs/agents.md` | Rewrite | Connecting agents in v2. |
| `README.md` | Rewrite | The v2 README. |

## Success criteria coverage

| Criterion (spec 2) | Test | Not automated, and why |
|---|---|---|
| 1. The chat is the hub | `TestAcceptance_1_ChatIsTheHub`, `TestChatHubEndToEnd`, `TestStopHookPerChat` | That Claude Code wakes an idle chat when a background command exits is the client's behaviour (Phase 0, spec 12, checked by hand in the terminal and the VS Code extension). The tests prove the listener process exits with the right line. |
| 2. Session-to-session links | `TestAcceptance_2_SessionLinks`, `TestScenario_MacAndGPUBox`, `TestCrossSessionIsolation` | |
| 3. Presence | `TestAcceptance_3_Presence`, `TestSessionCloseReachesPeerWithinFiveSeconds`, `TestPresenceTimeoutWhenMachineDrops` | The 150 second drop runs on a fake clock; a real sleep of a laptop is not simulated beyond a relay outage. |
| 4. Existing or new | `TestAcceptance_4_ExistingOrNew`, `TestManagedSessionEndToEnd` | Real Claude Code runs are the gated `CRAVV_CLAUDE_TEST=1` tests (`TestClaudeSmoke`, `TestClaudeContainment`). |
| 5. One approval | `TestAcceptance_5_OneApproval`, `TestReviewWithConfirmationCode` | Real forms in Claude Code and the VS Code extension's auto-decline are simulated by an MCP client that answers or declines. |
| 6. Setup | `TestAcceptance_6_Setup`; the one-liner and the release in `scripts/install_test.go` and `cmd/cravv-connect/release_test.go` | launchd and systemd, real PAM and a fresh machine without Go are replaced by seams; the install script is tested against a fake release server, not on a clean machine. |
| 7. UI | `TestAcceptance_7_UI`, `TestWebUIConnectAndAccept`, `TestWebUIKillAndResume` | A real browser is the gated `CRAVV_BROWSER_TEST=1` test. |
| 8. Security | `TestAcceptance_8_SecurityHolds`, `TestRelayNeverSeesPlaintext`, `TestManagedRunTokenIsScoped`, `TestManagedKillStopsRuns` | What another local user or a Full Disk Access process can read is documented, not tested. |

## Follow-ups (not in this phase)

- The CLI could gain `link disconnect` and `link restrict` (no password), and `link permit`'s help could say it always asks for the password; `files accept`'s help should drop "chat-only peer". These are production changes, left out to merge cleanly with the Phase 3 review.
- The link-scoped JSON CLI for agents without MCP (spec 3.1) is still deferred.
- The web UI's Devices page could show join codes and their QR code (spec 9).

---

### Task 1: e2e: acceptance criteria 1 to 3 (the chat hub with a real listener process, session links and isolation, presence)

The first three success criteria of spec section 2, each as one test named `TestAcceptance_<N>_<Name>` with the criterion quoted above it. Criterion 1 needs a real `cravv-connect listen` process, as Claude Code runs it in the background: `TestMain` lets the test binary itself run as the command line (`CRAVV_E2E_AS_CLI=1`), so no binary is built. The presence test drives both daemons on one `core.FakeClock` and stops the relay, so 150 seconds pass without waiting for them.

**Files:**
- Create: `e2e/cliproc_test.go`, `e2e/acceptance_test.go`
- Modify: `e2e/main_test.go`
- Test: the two new files are the tests

**Interfaces:**

Consumes (existing e2e helpers): `NewPair`, `NewPairWithClock`, `Node.Share`, `Node.Reattach`, `LinkUp`, `LinkChats`, `Connect`, `Node.Decide`, `Node.WaitLink`, `Node.Link`, `Node.Hook`, `WaitItem`, `Inbox`, `sendChat`, `wantKind`, `Waited`, `Silent`, `CLIRun`, `newClaudeAgent`, `Human`, `Choose`, `formChoices`, `mcpAgent.call/decode/try`, `Relay.Stop/Start`, `Daemon.Presence().Tick`.

Produces:
- `func cliProcessMain()` (called from `TestMain`), `func (n *Node) ProcessCLI(args ...string) <-chan CLIRun`, `func (n *Node) ListenerProcess(t *testing.T, listener string) <-chan CLIRun`.
- `func shareChat(t *testing.T, m *mcpAgent, name, visibility string) string` (returns the listener command), `func taskState(t *testing.T, c *ipc.Client, id, state string) ipc.TaskView`.
- `TestAcceptance_1_ChatIsTheHub`, `TestAcceptance_2_SessionLinks`, `TestAcceptance_3_Presence`.

- [ ] **Step 1: Let the test binary run as the command line**

Replace the whole of `e2e/main_test.go` with:

```go
package e2e

import (
	"os"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/fakeagent"
)

// TestMain lets this test binary run as the fake claude of managed runs
// (the managed tests point CRAVV_CLAUDE at it) and as the cravv-connect
// command line (ProcessCLI starts it that way).
func TestMain(m *testing.M) {
	fakeagent.Main()
	cliProcessMain()
	os.Exit(m.Run())
}
```

Create `e2e/cliproc_test.go`:

```go
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
```

- [ ] **Step 2: Write the acceptance tests for criteria 1 to 3**

Create `e2e/acceptance_test.go`:

```go
package e2e

// The v2 acceptance suite: one test per success criterion of spec section 2
// (docs/superpowers/specs/2026-09-26-cravv-connect-v2-sessions-design.md),
// each against real daemons on an in-process relay, through the surfaces a
// person and an agent use (the MCP server, the listener process, the CLI,
// the web UI). The criterion each test pins is quoted above it.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// shareChat shares an MCP chat and returns the listener command it got.
func shareChat(t *testing.T, m *mcpAgent, name, visibility string) string {
	t.Helper()
	var out struct {
		Listener  string `json:"listener"`
		WakeToken string `json:"wake_token"`
	}
	m.decode("session_share", map[string]any{"name": name, "purpose": name + " work", "visibility": visibility}, &out)
	if out.Listener == "" || out.WakeToken != "" {
		t.Fatalf("session_share returned %+v", out)
	}
	return out.Listener
}

// taskState polls a task on c until it reaches state.
func taskState(t *testing.T, c *ipc.Client, id, state string) ipc.TaskView {
	t.Helper()
	var tv ipc.TaskView
	Eventually(t, wait, "task "+id+" "+state, func() bool {
		Call(t, c, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: id}, &tv)
		return tv.State == state
	})
	return tv
}

// TestAcceptance_1_ChatIsTheHub pins criterion 1, "The chat is the hub":
// "Everything addressed to a session shows up in that session's chat
// without the human asking, including while the chat is idle." and
// "Accepting and rejecting happens in the chat for everything except the
// few actions that need a password (section 10)."
//
// The idle chat is a real `cravv-connect listen` process started from the
// command session_share returned, as Claude Code runs it in the
// background; its exit is what wakes the chat.
func TestAcceptance_1_ChatIsTheHub(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	human := &Human{}
	trainer, _ := newClaudeAgent(t, gpu, "chat-trainer", human)
	lead, _ := newClaudeAgent(t, mac, "chat-lead", nil)
	listener := shareChat(t, trainer, "trainer", "all-peers")
	shareChat(t, lead, "lead", "private")

	// A link request wakes the idle chat, which decides it in a form.
	idle := gpu.ListenerProcess(t, listener)
	Silent(t, idle, 300*time.Millisecond, "nothing addressed to the session yet")
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-ask", "note": "ACC1-NOTE"}, &out)
	in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	r := Waited(t, idle, wait, "the link request wakes the chat")
	if want := fmt.Sprintf("cravv-connect: 1 link request on link %d from alice. Call check_inbox, then review_pending.\n", in.Link); r.Code != 0 || r.Stdout != want {
		t.Fatalf("listener %+v, want %q", r, want)
	}
	trainer.call("check_inbox", nil)
	human.Answer(Choose("accept"))
	if text := trainer.call("review_pending", nil); !strings.Contains(text, "accepted") {
		t.Fatalf("review_pending %q", text)
	}
	mac.WaitLink(wait, "accepted in the chat", func(l ipc.LinkView) bool {
		return l.Link == out.Link && l.State == "active" && l.PermissionOut == "tasks-ask"
	})

	// A message wakes it again; the chat reads it wrapped.
	idle = gpu.ListenerProcess(t, listener)
	Silent(t, idle, 300*time.Millisecond, "everything read")
	lead.call("send_message", map[string]any{"link": out.Link, "text": "ACC1-HELLO"})
	r = Waited(t, idle, wait, "the message wakes the chat")
	if want := fmt.Sprintf("cravv-connect: 1 new message on link %d from alice. Call check_inbox.\n", in.Link); r.Stdout != want {
		t.Fatalf("listener %q, want %q", r.Stdout, want)
	}
	if text := trainer.call("check_inbox", nil); !strings.Contains(text, "<remote_message") || !strings.Contains(text, "ACC1-HELLO") {
		t.Fatalf("check_inbox %q", text)
	}

	// A task that needs approval wakes it; the human rejects it in the chat.
	idle = gpu.ListenerProcess(t, listener)
	var task ipc.TaskCreateResult
	lead.decode("create_task", map[string]any{"link": out.Link, "instructions": "ACC1-TASK wipe the cache"}, &task)
	r = Waited(t, idle, wait, "the task wakes the chat")
	if !strings.Contains(r.Stdout, fmt.Sprintf("1 task awaiting approval on link %d from alice", in.Link)) {
		t.Fatalf("listener %q", r.Stdout)
	}
	trainer.call("check_inbox", nil)
	human.Answer(Choose("reject"))
	if text := trainer.call("review_pending", nil); !strings.Contains(text, "denied") {
		t.Fatalf("review_pending %q", text)
	}
	Eventually(t, wait, "the sender sees the rejection", func() bool {
		return strings.Contains(lead.call("get_task", map[string]any{"task_id": task.TaskID}), `"state": "rejected"`)
	})

	// Without the listener (an agent that did not re-arm it) the prompt
	// hook still names what arrived and asks for the listener again, so
	// delivery degrades to the next turn.
	lead.call("send_message", map[string]any{"link": out.Link, "text": "ACC1-AGAIN"})
	arrived := fmt.Sprintf("cravv-connect: 1 new message on link %d from alice.", in.Link)
	var notice string
	Eventually(t, wait, "the prompt notice", func() bool {
		notice = gpu.Hook("UserPromptSubmit", "chat-trainer", false)
		return strings.HasPrefix(notice, arrived)
	})
	if !strings.Contains(notice, "The listener for this chat's session is not running") || strings.Contains(notice, "ACC1-AGAIN") {
		t.Fatalf("prompt notice %q", notice)
	}
	trainer.call("check_inbox", nil)

	// Agents without the listener poll with wait_for_message.
	trainer.call("send_message", map[string]any{"link": in.Link, "text": "ACC1-REPLY"})
	Eventually(t, wait, "wait_for_message returns the reply", func() bool {
		return strings.Contains(lead.call("wait_for_message", map[string]any{"timeout_s": 5}), "ACC1-REPLY")
	})

	// Granting tasks-auto is one of the password actions: the chat's form
	// never offers it and says where the password goes.
	second, _ := newClaudeAgent(t, mac, "chat-second", nil)
	shareChat(t, second, "second", "private")
	idle = gpu.ListenerProcess(t, listener)
	var auto ipc.LinkView
	second.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-auto"}, &auto)
	Waited(t, idle, wait, "the tasks-auto request wakes the chat")
	trainer.call("check_inbox", nil)
	human.Answer(Choose("accept as tasks-ask"))
	trainer.call("review_pending", nil)
	forms := human.Forms()
	form := forms[len(forms)-1]
	if !strings.Contains(form.Message, "Granting tasks-auto needs your password: run cravv-connect link accept") {
		t.Fatalf("form %q", form.Message)
	}
	for _, c := range formChoices(t, form) {
		if strings.Contains(c, "tasks-auto") {
			t.Fatalf("the chat's form offers %q", c)
		}
	}
	granted := mac.WaitLink(wait, "accepted lower", func(l ipc.LinkView) bool { return l.Link == auto.Link && l.State == "active" })
	if granted.PermissionOut != "tasks-ask" {
		t.Fatalf("the chat granted %q", granted.PermissionOut)
	}
	raise := gpu.WaitLink(wait, "the second link", func(l ipc.LinkView) bool { return l.RemoteSession == "second" && l.State == "active" })
	wantKind(t, TryCall(gpu.Conn(), ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: raise.Link, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
	Call(t, gpu.Unlocked(), ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: raise.Link, Permission: "tasks-auto"}, nil)
	mac.WaitLink(wait, "raised with the password", func(l ipc.LinkView) bool { return l.Link == auto.Link && l.PermissionOut == "tasks-auto" })
}

// TestAcceptance_2_SessionLinks pins criterion 2, "Session-to-session
// links": "Two sessions can only exchange messages, tasks or files over an
// accepted link.", "A session may hold several links." and "Traffic on one
// link is never visible to another session."
//
// Two sessions per machine: mac/lead and mac/notes, gpu/trainer and
// gpu/helper. lead links to both GPU sessions; notes links to none.
func TestAcceptance_2_SessionLinks(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	lead := mac.Share("claude", "lead", "private")
	notes := mac.Share("codex", "notes", "private")
	trainer := gpu.Share("claude", "trainer", "all-peers")
	helper := gpu.Share("codex", "helper", "all-peers")
	if err := os.WriteFile(filepath.Join(mac.Proj, "acc2.txt"), []byte("ACC2-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before the link is accepted nothing can be sent on it.
	pending := Connect(t, lead, "bob/trainer", "tasks-auto", "")
	wantKind(t, TryCall(lead.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: pending.Link, Text: "too early"}, nil), ipc.KindLinkClosed)
	wantKind(t, TryCall(lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: pending.Link, Instructions: "too early"}, nil), ipc.KindLinkClosed)
	wantKind(t, TryCall(lead.C, ipc.MethodFileSend, ipc.FileSendParams{Link: pending.Link, Path: "acc2.txt"}, nil), ipc.KindLinkClosed)
	// Pairing alone links nothing: the machine's other session has no link.
	wantKind(t, TryCall(notes.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: pending.Link, Text: "not mine"}, nil), ipc.KindNotFound)
	in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Session == "trainer" })
	gpu.Decide(in.Link, true, "")
	mac.WaitLink(wait, "link one active", func(l ipc.LinkView) bool { return l.Link == pending.Link && l.State == "active" })
	one := Linked{A: lead, B: trainer, ANum: pending.Link, BNum: in.Link}
	two := LinkChats(t, mac, gpu, lead, helper, "tasks-auto")

	var mine ipc.LinksResult
	Call(t, lead.C, ipc.MethodLinks, nil, &mine)
	if len(mine.Links) != 2 || mine.Links[0].State != "active" || mine.Links[1].State != "active" {
		t.Fatalf("lead holds %+v, want two active links", mine.Links)
	}
	for _, s := range []*SharedChat{lead, trainer, helper} {
		Inbox(t, s.C) // the link notices
	}

	// Traffic on link one: a message, a task and a file.
	msg := sendChat(t, lead.C, one.ANum, "ACC2-CHAT-ONE")
	var task ipc.TaskCreateResult
	Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: one.ANum, Instructions: "ACC2-TASK-ONE"}, &task)
	var file ipc.FileSendResult
	Call(t, lead.C, ipc.MethodFileSend, ipc.FileSendParams{Link: one.ANum, Path: "acc2.txt"}, &file)
	var got []ipc.InboxView
	Eventually(t, wait, "link one's traffic at trainer", func() bool {
		got = append(got, Inbox(t, trainer.C)...)
		var chat, tsk, f bool
		for _, it := range got {
			chat = chat || it.ID == msg
			tsk = tsk || it.TaskID == task.TaskID
			f = f || (it.FileID == file.FileID && it.Path != "")
		}
		return chat && tsk && f
	})
	// ... and on link two, only helper's.
	msg2 := sendChat(t, lead.C, two.ANum, "ACC2-CHAT-TWO")
	WaitItem(t, helper.C, wait, "link two's message at helper", isChat(msg2))
	for _, it := range got {
		if strings.Contains(it.Wrapped, "ACC2-CHAT-TWO") {
			t.Fatalf("trainer saw link two's message: %+v", it)
		}
	}
	for _, it := range Inbox(t, helper.C) {
		if strings.Contains(it.Wrapped, "ACC2-") && !strings.Contains(it.Wrapped, "ACC2-CHAT-TWO") {
			t.Fatalf("helper saw link one's traffic: %+v", it)
		}
	}
	if items := Inbox(t, notes.C); len(items) != 0 {
		t.Fatalf("notes, with no link, saw %+v", items)
	}

	// Cross-talk is impossible, not just hidden: other sessions cannot
	// reach link one's task or send on its number, on either machine.
	for name, c := range map[string]*ipc.Client{"helper": helper.C, "notes": notes.C} {
		wantKind(t, TryCall(c, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: task.TaskID}, nil), ipc.KindNotFound)
		wantKind(t, TryCall(c, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: task.TaskID}, nil), ipc.KindNotFound)
		if err := TryCall(c, ipc.MethodTaskCancel, ipc.TaskIDParams{TaskID: task.TaskID}, nil); !ipc.IsKind(err, ipc.KindNotFound) {
			t.Fatalf("%s cancelled link one's task: %v", name, err)
		}
	}
	wantKind(t, TryCall(helper.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: one.BNum, Text: "hijack"}, nil), ipc.KindNotFound)
	wantKind(t, TryCall(notes.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: one.ANum, Text: "hijack"}, nil), ipc.KindNotFound)
	Call(t, trainer.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: task.TaskID}, nil)
	Call(t, trainer.C, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: task.TaskID, Result: "ACC2-RESULT"}, nil)
	taskState(t, lead.C, task.TaskID, "done")
	for _, it := range Inbox(t, notes.C) {
		t.Fatalf("notes saw the task's updates: %+v", it)
	}
}

// TestAcceptance_3_Presence pins criterion 3, "Presence": "When a linked
// session closes, the other side learns within 5 seconds (both machines
// online), or within 150 seconds when a machine drops." and "A session
// that is only briefly away (sleep, MCP reconnect) does not lose its
// links."
func TestAcceptance_3_Presence(t *testing.T) {
	t.Parallel()

	t.Run("a closed session reaches the peer within 5 seconds", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		l := LinkUp(t, mac, gpu, "messages")
		start := time.Now()
		Call(t, l.B.C, ipc.MethodSessionClose, nil, nil)
		mac.WaitLink(5*time.Second, "session_closed", func(v ipc.LinkView) bool {
			return v.Link == l.ANum && v.State == "closed" && v.Reason == core.CloseSessionClosed
		})
		t.Logf("the Mac learned of the close after %s", time.Since(start).Round(time.Millisecond))
		WaitItem(t, l.A.C, 5*time.Second, "the Mac's chat is told", func(it ipc.InboxView) bool {
			return it.Link == l.ANum && strings.Contains(it.Wrapped, "closed")
		})
	})

	t.Run("a machine that drops closes its links within 150 seconds", func(t *testing.T) {
		t.Parallel()
		clock := core.NewFakeClock(time.Now())
		r, mac, gpu := NewPairWithClock(t, clock)
		l := LinkUp(t, mac, gpu, "messages")
		ctx := context.Background()
		tick := func() {
			for _, n := range []*Node{mac, gpu} {
				if err := n.Daemon.Presence().Tick(ctx); err != nil {
					t.Fatal(err)
				}
			}
		}
		// The relay goes first, so no presence frame from before the drop
		// can arrive late; the links' timeouts start at the first tick.
		r.Stop()
		tick()
		for range 5 { // 150 seconds of pings nobody receives
			clock.Advance(core.PresenceInterval)
			tick()
		}
		if a, b := mac.Link(l.ANum), gpu.Link(l.BNum); a.State != "active" || b.State != "active" {
			t.Fatalf("closed before 150 seconds: %+v %+v", a, b)
		}
		clock.Advance(time.Second)
		tick()
		for _, v := range []ipc.LinkView{mac.Link(l.ANum), gpu.Link(l.BNum)} {
			if v.State != "closed" || v.Reason != core.ClosePresenceTimeout {
				t.Fatalf("after 150 seconds: %+v", v)
			}
		}
	})

	t.Run("a briefly away session keeps its links", func(t *testing.T) {
		t.Parallel()
		clock := core.NewFakeClock(time.Now())
		r, mac, gpu := NewPairWithClock(t, clock)
		l := LinkUp(t, mac, gpu, "messages")
		ctx := context.Background()
		tick := func(n *Node) {
			if err := n.Daemon.Presence().Tick(ctx); err != nil {
				t.Fatal(err)
			}
		}

		// MCP reconnect: the chat's connection drops, the session is away,
		// what is sent meanwhile waits, and a reattach brings it all back.
		l.B.C.Close()
		mac.WaitLink(wait, "the peer is away", func(v ipc.LinkView) bool { return v.Link == l.ANum && v.RemoteAway && v.State == "active" })
		queued := sendChat(t, l.A.C, l.ANum, "ACC3-WHILE-AWAY")
		back := gpu.Reattach("claude", l.B)
		mac.WaitLink(wait, "the peer is back", func(v ipc.LinkView) bool { return v.Link == l.ANum && !v.RemoteAway && v.State == "active" })
		WaitItem(t, back.C, wait, "what queued while away", isChat(queued))

		// Sleep: both machines lose the relay for less than the timeout.
		r.Stop()
		tick(mac)
		tick(gpu)
		clock.Advance(4 * core.PresenceInterval) // 120 seconds
		tick(mac)
		tick(gpu)
		r.Start()
		mac.WaitOnline()
		gpu.WaitOnline()
		// Presence resumes: the Mac's ping reaches the GPU box before the
		// chat sent after it, and the GPU box's pong reaches the Mac before
		// the reply sent after that (one mailbox, in order).
		tick(mac)
		ping := sendChat(t, l.A.C, l.ANum, "ACC3-AWAKE")
		WaitItem(t, back.C, wait, "the chat after the ping", isChat(ping))
		tick(gpu)
		pong := sendChat(t, back.C, l.BNum, "ACC3-AWAKE-TOO")
		WaitItem(t, l.A.C, wait, "the reply after the pong", isChat(pong))
		clock.Advance(4 * core.PresenceInterval) // 240 seconds since the drop
		tick(mac)
		tick(gpu)
		if a, b := mac.Link(l.ANum), gpu.Link(l.BNum); a.State != "active" || b.State != "active" {
			t.Fatalf("a brief absence closed the link: %+v %+v", a, b)
		}
	})
}
```

- [ ] **Step 3: Run them**

```sh
go test ./e2e -run 'TestAcceptance_[123]' -race -count=1 -v 2>&1 | grep -E '^(---|    ---|ok|FAIL)'
```

Expected: `--- PASS` for `TestAcceptance_1_ChatIsTheHub`, `TestAcceptance_2_SessionLinks`, `TestAcceptance_3_Presence` and its three subtests, then `ok`. The behaviour exists since Phases 1 to 5; these tests pin it per criterion.

- [ ] **Step 4: Watch the presence test fail without pongs**

Temporarily delete the two lines `tick(mac)` (before `ping := sendChat`) and `tick(gpu)` (before `pong := sendChat`) in the subtest "a briefly away session keeps its links", then:

```sh
go test ./e2e -run 'TestAcceptance_3' -count=1 2>&1 | grep -E 'a brief absence|FAIL' | head -3
```

Expected: `a brief absence closed the link: ...` and `FAIL`: without the ping and pong after the outage the links time out, so the test really depends on presence resuming. Restore the two lines (the file must match Step 2 again).

- [ ] **Step 5: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 6: Commit**

```sh
git add e2e/acceptance_test.go e2e/cliproc_test.go e2e/main_test.go
git commit -F - <<'MSG'
e2e: acceptance criteria 1 to 3: the chat hub with a real listener process, session links and isolation, presence

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 2: e2e: acceptance criteria 4 and 5 (an open chat or a managed session with nobody there; one decision per link and task; the sender sees seen)

Criterion 4 links a Mac chat to a chat the GPU box has open and to a new managed session started from an offer, with the fake agent (the test binary) as `claude`, and checks that nothing asked the GPU box's human. Criterion 5 decides a link once in the accepting chat's form and a `tasks-ask` task once with the confirmation code (the form is dismissed, as the VS Code extension does), and follows the task on the sender's side through `awaiting_approval`, `queued`, `seen`, `claimed`, `running` and `done`.

**Files:**
- Create: `e2e/acceptance_remote_test.go`
- Test: the new file is the test

**Interfaces:**

Consumes: Task 1's `shareChat`; the Phase 3 e2e helpers `newManagedPair`, `managedPair.runs`; `newClaudeAgent`, `Human`, `Choose`, `Dismiss`, `Desktop.Code`, `Desktop.Last`.

Produces: `TestAcceptance_4_ExistingOrNew` (not parallel: it sets the fake agent's environment) and `TestAcceptance_5_OneApproval`.

- [ ] **Step 1: Write the acceptance tests for criteria 4 and 5**

Create `e2e/acceptance_remote_test.go`:

```go
package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// TestAcceptance_4_ExistingOrNew pins criterion 4, "Existing or new": "A
// host can link to a session the remote side already has open." and "A
// host can also ask the remote daemon to start a managed session in a
// folder the remote owner pre-approved. This works with no human at the
// remote machine."
//
// The GPU box's agent is the fake claude (this test binary), so the runs
// are real processes without calling Claude. Not parallel: it sets the
// fake agent's environment.
func TestAcceptance_4_ExistingOrNew(t *testing.T) {
	p := newManagedPair(t, "reply", ipc.OfferSetParams{Label: "trainer", Permission: "tasks-auto", RunMode: "edit-in-folder"})
	lead, _ := newClaudeAgent(t, p.mac, "chat-lead", nil)
	shareChat(t, lead, "lead", "private")

	// Existing: a chat the GPU box's human has open.
	wakeword := p.gpu.Share("claude", "wakeword", "all-peers")
	var listed ipc.SessionsListResult
	lead.decode("sessions", map[string]any{"machine": "gpu-box"}, &listed)
	if len(listed.Sessions) != 1 || listed.Sessions[0].Name != "wakeword" || listed.Sessions[0].Kind != "live" {
		t.Fatalf("the Mac sees sessions %+v", listed.Sessions)
	}
	if len(listed.Offers) != 1 || listed.Offers[0].Label != "trainer" || listed.Offers[0].MaxPermission != "tasks-auto" {
		t.Fatalf("the Mac sees offers %+v", listed.Offers)
	}
	var existing ipc.LinkView
	lead.decode("connect", map[string]any{"target": "gpu-box/wakeword", "permission": "messages"}, &existing)
	in := p.gpu.WaitLink(wait, "the request to wakeword", func(l ipc.LinkView) bool { return l.State == "pending" && l.Session == "wakeword" })
	p.gpu.Decide(in.Link, true, "")
	p.mac.WaitLink(wait, "linked to the open chat", func(l ipc.LinkView) bool { return l.Link == existing.Link && l.State == "active" })
	lead.call("send_message", map[string]any{"link": existing.Link, "text": "ACC4-EXISTING"})
	WaitItem(t, wakeword.C, wait, "the open chat gets it", func(it ipc.InboxView) bool { return strings.Contains(it.Wrapped, "ACC4-EXISTING") })

	// New: the offer starts a managed session and accepts the link with
	// nobody at the GPU box (no decision, no notification).
	desktopBefore, _ := p.gpu.Desktop.Last()
	var created ipc.LinkView
	lead.decode("connect", map[string]any{"target": "gpu-box/new:trainer", "permission": "tasks-auto"}, &created)
	link := p.mac.WaitLink(wait, "the managed link", func(l ipc.LinkView) bool { return l.Link == created.Link && l.State == "active" })
	if link.PermissionOut != "tasks-auto" || !strings.HasPrefix(link.RemoteSession, "trainer-") {
		t.Fatalf("managed link %+v", link)
	}
	var task ipc.TaskCreateResult
	lead.decode("create_task", map[string]any{"link": link.Link, "instructions": "ACC4-NEW count the lines"}, &task)
	Eventually(t, wait, "the managed session finished the task", func() bool {
		return strings.Contains(lead.call("get_task", map[string]any{"task_id": task.TaskID}), `"state": "done"`)
	})
	recs := p.runs(t, 1)
	if recs[0].Dir != p.folder || !strings.Contains(recs[0].Prompt, "ACC4-NEW count the lines") {
		t.Fatalf("the run %+v", recs[0])
	}
	if after, _ := p.gpu.Desktop.Last(); after != desktopBefore {
		t.Fatalf("the GPU box asked its human: %q", after)
	}

	// Only a folder the owner pre-approved: an unknown offer is not found.
	if text, isErr := lead.try("connect", map[string]any{"target": "gpu-box/new:elsewhere", "permission": "messages"}); !isErr || !strings.Contains(text, "not found") {
		t.Fatalf("connect to an unknown offer: %q", text)
	}
}

// TestAcceptance_5_OneApproval pins criterion 5, "One approval": "A link
// request is decided once, on the accepting side.", "A task under an "ask
// each time" link is decided once, on the receiving side." and "The
// sender always sees a task's state, including `seen`, so "stuck" and
// "slow" look different."
//
// The accepting chat's client shows forms (elicitation) for the link;
// for the task its human dismisses the form, as the VS Code extension
// does, and decides with the confirmation code from the desktop.
func TestAcceptance_5_OneApproval(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	human := &Human{}
	trainer, _ := newClaudeAgent(t, gpu, "chat-trainer", human)
	lead, _ := newClaudeAgent(t, mac, "chat-lead", nil)
	shareChat(t, trainer, "trainer", "all-peers")
	shareChat(t, lead, "lead", "private")

	// The link: one decision, by the accepting side's human, in a form.
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-ask", "note": "ACC5-NOTE"}, &out)
	gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	if text := lead.call("review_pending", nil); text != "Nothing is waiting for a decision." {
		t.Fatalf("the requester was asked: %q", text)
	}
	human.Answer(Choose("accept"))
	trainer.call("review_pending", nil)
	mac.WaitLink(wait, "active on both sides", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
	if n := len(human.Forms()); n != 1 {
		t.Fatalf("the accepting human saw %d forms, want 1", n)
	}
	if title, _ := mac.Desktop.Last(); title != "" {
		t.Fatalf("the requesting machine notified its human: %q", title)
	}
	trainer.call("check_inbox", nil)
	lead.call("check_inbox", nil)

	// The task: held for the receiving human, who decides once.
	var task ipc.TaskCreateResult
	lead.decode("create_task", map[string]any{"link": out.Link, "instructions": "ACC5-TASK train for one epoch"}, &task)
	state := func() string {
		var tv ipc.TaskView
		if err := json.Unmarshal([]byte(lead.call("get_task", map[string]any{"task_id": task.TaskID})), &tv); err != nil {
			t.Fatal(err)
		}
		return tv.State
	}
	Eventually(t, wait, "the sender sees awaiting_approval", func() bool { return state() == "awaiting_approval" })
	human.Answer(Dismiss("decline"))
	text := trainer.call("review_pending", nil)
	code := gpu.Desktop.Code()
	item := "task-" + task.TaskID
	if len(code) != 4 || !strings.Contains(text, item) || strings.Contains(text, code) {
		t.Fatalf("review_pending %q with code %q", text, code)
	}
	if text := trainer.call("review_pending", map[string]any{"item": item, "decision": "accept", "code": code}); !strings.Contains(text, "approved by the human") {
		t.Fatalf("the typed code %q", text)
	}
	// Approved, but its session has not read it yet: queued, not seen.
	Eventually(t, wait, "the sender sees queued", func() bool { return state() == "queued" })
	if text := trainer.call("check_inbox", nil); !strings.Contains(text, "ACC5-TASK") {
		t.Fatalf("check_inbox %q", text)
	}
	Eventually(t, wait, "the sender sees seen", func() bool { return state() == "seen" })

	// Nothing asks again: not the chat, not the human's terminal.
	if text := trainer.call("review_pending", nil); text != "Nothing is waiting for a decision." {
		t.Fatalf("asked again: %q", text)
	}
	var held ipc.ApprovalsListResult
	Call(t, gpu.Unlocked(), ipc.MethodApprovalsList, nil, &held)
	if len(held.Tasks) != 0 {
		t.Fatalf("the terminal still holds %+v", held.Tasks)
	}
	if n := len(human.Forms()); n != 2 {
		t.Fatalf("the human saw %d forms, want 2 (the link, the task)", n)
	}

	// The worker runs it; the sender sees every step.
	for _, step := range []struct{ tool, state string }{{"claim_task", "claimed"}, {"update_task", "running"}, {"complete_task", "done"}} {
		args := map[string]any{"task_id": task.TaskID}
		switch step.tool {
		case "update_task":
			args["note"] = "halfway"
		case "complete_task":
			args["result"] = "ACC5-RESULT"
		}
		trainer.call(step.tool, args)
		Eventually(t, wait, fmt.Sprintf("the sender sees %s", step.state), func() bool { return state() == step.state })
	}
}
```

- [ ] **Step 2: Run them**

```sh
go test ./e2e -run 'TestAcceptance_[45]' -race -count=1 -v 2>&1 | grep -E '^(---|ok|FAIL)'
```

Expected: `--- PASS: TestAcceptance_4_ExistingOrNew`, `--- PASS: TestAcceptance_5_OneApproval`, `ok`.

- [ ] **Step 3: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 4: Commit**

```sh
git add e2e/acceptance_remote_test.go
git commit -F - <<'MSG'
e2e: acceptance criteria 4 and 5: an open chat or a managed session with nobody there, one decision per link and task, the sender sees seen

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 3: e2e: acceptance criterion 6 (setup and setup --join take two fresh machines to paired and chat-connected against a real relay)

Two machines with empty state and home folders run the real `cravv-connect setup` and `setup --join <code>` (`cli.Main`) against a real in-process relay. The only seams are the ones setup already has: a scripted `Prompter` that fails on any question it did not expect, a `SetupSystem` whose health check reaches the in-process relay, a service manager that starts the real daemon in this process from the `config.toml` and `store.db` setup wrote, and the Claude Code installer with a recorded `claude` command. The relay needs a URL that is neither loopback (setup would say another machine cannot reach it and not offer pairing) nor public plain http (join codes refuse it): `NewLANRelay` names it `http://cravv-relay.local:<port>` and daemons reach it through `Relay.HTTPClient`, which dials its real address.

**Files:**
- Create: `e2e/acceptance_setup_test.go`
- Modify: `e2e/harness.go` (`Relay.origin`, `NewLANRelay`, `LANRelayHost`, `Relay.HTTPClient`)
- Test: the new file is the test

**Interfaces:**

Consumes: `cli.Main`, `cli.Env` (`Prompt`, `Paths`, `Dial`, `Getwd`, `Hostname`, `OpenSettings`, `Executable`, `Service`, `ServiceSetup`, `Agents`, `Setup`), `cli.SetupSystem`, `install.NewRegistry`, `install.Claude`, `install.Codex`, `relayclient.New`, `relayclient.WithHTTPClient`, `daemon.Options.Relay`, `Node.start`, `newClaudeAgent`, Task 1's `shareChat`.

Produces:
- `func NewLANRelay(t *testing.T) *Relay`, `const LANRelayHost`, `func (r *Relay) HTTPClient() *http.Client`; `Relay.URL` returns the origin when set.
- `freshMachine` (`newFreshMachine`, `Run`, `JoinCode`, `Node`; it is its own `cli.ServiceManager` and `cli.ServiceInstaller`), `scriptPrompter`, `askLine`, `askPassword`, `claudeCLI`, `lanSystem`, `syncBuffer`.
- `TestAcceptance_6_Setup`.

- [ ] **Step 1: Give the harness a relay with a LAN name**

Apply this patch to `e2e/harness.go` (`git apply`):

```diff
diff --git a/e2e/harness.go b/e2e/harness.go
index 51da037..97382cd 100644
--- a/e2e/harness.go
+++ b/e2e/harness.go
@@ -40,9 +40,10 @@ const AdminToken = "e2e-admin-token"
 // the same address with the same backend, like a relay restart that keeps
 // its storage.
 type Relay struct {
-	t    *testing.T
-	addr string
-	srv  *relayserver.Server
+	t      *testing.T
+	addr   string
+	origin string // the URL clients use, when it is not http://addr
+	srv    *relayserver.Server
 
 	mu    sync.Mutex
 	hs    *http.Server
@@ -56,12 +57,34 @@ func NewRelay(t *testing.T) *Relay { t.Helper(); return NewRelayWith(t, nil) }
 // NewRelayWith is NewRelay with the memory backend passed through wrap (when
 // not nil), so a test can observe or slow down what the relay stores.
 func NewRelayWith(t *testing.T, wrap func(relayserver.Backend) relayserver.Backend) *Relay {
+	t.Helper()
+	return newRelay(t, wrap, "")
+}
+
+// LANRelayHost is the host name of NewLANRelay's URL.
+const LANRelayHost = "cravv-relay.local"
+
+// NewLANRelay is NewRelay under the kind of URL a LAN test relay has,
+// http://cravv-relay.local:<port>: a private-network address that join
+// codes accept and setup does not treat as reachable only from this
+// machine. Nothing resolves the name; clients reach the relay through
+// HTTPClient.
+func NewLANRelay(t *testing.T) *Relay {
+	t.Helper()
+	return newRelay(t, nil, LANRelayHost)
+}
+
+func newRelay(t *testing.T, wrap func(relayserver.Backend) relayserver.Backend, host string) *Relay {
 	t.Helper()
 	ln, err := net.Listen("tcp", "127.0.0.1:0")
 	if err != nil {
 		t.Fatal(err)
 	}
 	r := &Relay{t: t, addr: ln.Addr().String()}
+	if host != "" {
+		_, port, _ := net.SplitHostPort(r.addr)
+		r.origin = "http://" + net.JoinHostPort(host, port)
+	}
 	var backend relayserver.Backend = relayserver.NewMemoryBackend(core.SystemClock{})
 	if wrap != nil {
 		backend = wrap(backend)
@@ -82,7 +105,23 @@ func NewRelayWith(t *testing.T, wrap func(relayserver.Backend) relayserver.Backe
 }
 
 // URL is the relay base URL daemons are configured with.
-func (r *Relay) URL() string { return "http://" + r.addr }
+func (r *Relay) URL() string {
+	if r.origin != "" {
+		return r.origin
+	}
+	return "http://" + r.addr
+}
+
+// HTTPClient reaches the relay at its listening address whatever host its
+// URL names.
+func (r *Relay) HTTPClient() *http.Client {
+	var d net.Dialer
+	return &http.Client{Transport: &http.Transport{
+		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
+			return d.DialContext(ctx, network, r.addr)
+		},
+	}}
+}
 
 func (r *Relay) serve(ln net.Listener) {
 	r.mu.Lock()
```

- [ ] **Step 2: Write the setup acceptance test**

Create `e2e/acceptance_setup_test.go`:

```go
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/auth"
	"github.com/cookwithcravv/cravv-connect/internal/cli"
	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/daemon"
	"github.com/cookwithcravv/cravv-connect/internal/install"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/store"
	"github.com/cookwithcravv/cravv-connect/internal/store/sqlite"
	"github.com/cookwithcravv/cravv-connect/internal/transport/relayclient"
)

// installedBinary is where the fresh machines' cravv-connect is installed
// (install.sh puts it in ~/.local/bin; any path outside the temporary
// folder does, since setup refuses to install a temporary build).
const installedBinary = "/home/tester/.local/bin/cravv-connect"

// syncBuffer is a bytes.Buffer the test can read while a command writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// promptStep is one question the person at a fresh machine answers: the
// prompt must contain ask, and answer is typed.
type promptStep struct {
	password bool
	ask      string
	answer   string
}

func askLine(ask, answer string) promptStep { return promptStep{ask: ask, answer: answer} }
func askPassword(ask, answer string) promptStep {
	return promptStep{password: true, ask: ask, answer: answer}
}

// scriptPrompter answers setup's questions in order, and fails the answer
// (so setup stops) when a question is not the one expected next.
type scriptPrompter struct {
	mu    sync.Mutex
	steps []promptStep
	asked []string
}

func (p *scriptPrompter) next(password bool, prompt, def string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, prompt)
	if len(p.steps) == 0 {
		return "", fmt.Errorf("unexpected question %q", prompt)
	}
	s := p.steps[0]
	if s.password != password || !strings.Contains(prompt, s.ask) {
		return "", fmt.Errorf("asked %q, want a question about %q", prompt, s.ask)
	}
	p.steps = p.steps[1:]
	if s.answer == "" {
		return def, nil
	}
	return s.answer, nil
}

func (p *scriptPrompter) Password(prompt string) (string, error) { return p.next(true, prompt, "") }
func (p *scriptPrompter) Line(prompt, def string) (string, error) {
	return p.next(false, prompt, def)
}

// left returns the questions not asked yet.
func (p *scriptPrompter) left() []promptStep {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]promptStep(nil), p.steps...)
}

// claudeCLI stands in for the claude command the installer runs.
type claudeCLI struct {
	mu   sync.Mutex
	runs []string
}

func (c *claudeCLI) Run(_ context.Context, name string, args ...string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs = append(c.runs, name+" "+strings.Join(args, " "))
	return "", nil
}

// lanSystem is SetupSystem for the test relay: no name resolves and no
// relay is started; the health check goes to the in-process relay.
type lanSystem struct{ relay *Relay }

func (lanSystem) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("no such host")
}
func (lanSystem) PrivateAddrs() ([]netip.Addr, error) { return nil, nil }
func (s lanSystem) RelayHealthy(ctx context.Context, origin string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+relayproto.PathHealth, nil)
	if err != nil {
		return err
	}
	res, err := s.relay.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var h relayproto.Health
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&h) != nil || !h.OK {
		return errors.New("not a cravv relay")
	}
	return nil
}
func (lanSystem) StartRelay(string, []string, []string, string) error {
	return errors.New("no LAN relay in this test")
}

// freshMachine is a machine with nothing set up, for the real
// `cravv-connect setup`: an empty state folder, a home folder where Claude
// Code keeps its settings, and a login service that, when setup installs
// it, starts the real daemon in this process on the relay (instead of
// launchd or systemd). Only the password verifier, the identity store and
// the desktop differ from production, as for every e2e node.
type freshMachine struct {
	t      *testing.T
	name   string
	relay  *Relay
	paths  config.Paths
	home   string
	prompt *scriptPrompter
	out    *syncBuffer
	claude *claudeCLI

	mu   sync.Mutex
	node *Node // the daemon, once the service started it
}

func newFreshMachine(t *testing.T, r *Relay, name string) *freshMachine {
	t.Helper()
	dir, err := os.MkdirTemp("", "ccs-") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	state, home := filepath.Join(dir, "s"), filepath.Join(dir, "home")
	for _, d := range []string{state, filepath.Join(home, ".claude"), filepath.Join(home, "work", "proj")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return &freshMachine{
		t: t, name: name, relay: r, home: home, prompt: &scriptPrompter{}, out: &syncBuffer{}, claude: &claudeCLI{},
		paths: config.Paths{
			Home: state, Config: filepath.Join(state, "config.toml"), DB: filepath.Join(state, "store.db"),
			Audit: filepath.Join(state, "audit.log"), Socket: filepath.Join(state, "d.sock"),
			Files: filepath.Join(state, "files"), Log: filepath.Join(state, "daemon.log"),
		},
	}
}

// Installed, Start and Stop make the machine its own service manager.
func (m *freshMachine) Installed() bool { return true }
func (m *freshMachine) Start(ctx context.Context) error {
	return m.Install(ctx, installedBinary)
}
func (m *freshMachine) Stop(context.Context) error {
	m.mu.Lock()
	n := m.node
	m.node = nil
	m.mu.Unlock()
	if n != nil {
		n.Stop()
	}
	return nil
}
func (m *freshMachine) Uninstall(ctx context.Context) error { return m.Stop(ctx) }

// Install starts the daemon from what setup wrote (config.toml, store.db).
func (m *freshMachine) Install(_ context.Context, bin string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.node != nil {
		return nil
	}
	if bin != installedBinary {
		return fmt.Errorf("service for %q, want %q", bin, installedBinary)
	}
	cfg, err := config.Load(m.paths)
	if err != nil {
		return err
	}
	rc, err := relayclient.New(cfg.RelayURL, relayclient.WithHTTPClient(m.relay.HTTPClient()))
	if err != nil {
		return err
	}
	desk := &Desktop{}
	opts := daemon.Options{
		Paths: m.paths, Config: cfg, Clock: core.SystemClock{}, Relay: rc,
		Verifier: auth.Fake{Password: Password}, Desktop: desk, Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore {
			return daemon.SettingsIdentityStore{Settings: s}
		},
		ReconnectMin: 50 * time.Millisecond, MaintenanceEvery: time.Hour, FileRetryDelay: 100 * time.Millisecond,
	}
	d, err := daemon.New(opts)
	if err != nil {
		return err
	}
	n := &Node{t: m.t, Name: m.name, Dir: m.paths.Home, Proj: filepath.Join(m.home, "work", "proj"), Paths: m.paths,
		Daemon: d, Clock: core.SystemClock{}, Desktop: desk, opts: opts}
	n.start()
	m.node = n
	return nil
}

// Node returns the machine's daemon once setup started it.
func (m *freshMachine) Node() *Node {
	m.t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.node == nil {
		m.t.Fatalf("%s: setup did not start the daemon", m.name)
	}
	return m.node
}

// env is the command line's environment on this machine: its folders,
// terminal (the script), agents (Claude Code found, Codex not) and the
// service manager above.
func (m *freshMachine) env() *cli.Env {
	agents := install.NewRegistry(
		&install.Claude{Home: m.home, Run: m.claude, LookPath: func(name string) (string, error) {
			if name == "claude" {
				return "/home/tester/.local/bin/claude", nil
			}
			return "", errors.New("not found")
		}},
		&install.Codex{Home: m.home, LookPath: func(string) (string, error) { return "", errors.New("not found") }},
	)
	return &cli.Env{
		Stdin: strings.NewReader(""), Stdout: m.out, Stderr: m.out, Prompt: m.prompt,
		Paths:    func() (config.Paths, error) { return m.paths, nil },
		Dial:     func(ctx context.Context) (cli.Caller, error) { return ipc.DialContext(ctx, m.paths.Socket) },
		Getwd:    func() (string, error) { return m.home, nil },
		Hostname: func() (string, error) { return m.name + ".lan", nil },
		OpenSettings: func(dbPath string) (store.SettingsStore, func() error, error) {
			db, err := sqlite.Open(dbPath)
			if err != nil {
				return nil, nil, err
			}
			return db, db.Close, nil
		},
		Executable: func() (string, error) { return installedBinary, nil },
		Service:    m, ServiceSetup: m, Agents: agents, Setup: lanSystem{m.relay},
	}
}

// Run runs `cravv-connect args...` on this machine and returns its exit
// code; the output accumulates in m.out.
func (m *freshMachine) Run(args ...string) int { return cli.Main(args, m.env()) }

var joinCodeLine = regexp.MustCompile(`cravv-connect setup --join (cravv-join:\S+)`)

// JoinCode waits for the join code setup shows on this machine.
func (m *freshMachine) JoinCode() string {
	m.t.Helper()
	var code string
	Eventually(m.t, wait, m.name+" shows a join code", func() bool {
		if s := joinCodeLine.FindStringSubmatch(m.out.String()); s != nil {
			code = s[1]
		}
		return code != ""
	})
	return code
}

// TestAcceptance_6_Setup pins criterion 6, "Setup": "A fresh machine goes
// from nothing to paired and chat-connected with an install one-liner plus
// one command (`cravv-connect setup`, or `setup --join <code>`)." and "No
// Go toolchain is needed."
//
// Two fresh machines with empty state and home folders run the real setup
// wizard against a real relay: the Mac hosts (relay, admin token, daemon,
// Claude Code, pair a device now) and the GPU box joins with the code the
// Mac shows. Nothing but the one command runs on either machine. The
// install one-liner and the release binaries (no Go toolchain) are pinned
// by scripts/install_test.go and cmd/cravv-connect/release_test.go.
func TestAcceptance_6_Setup(t *testing.T) {
	t.Parallel()
	r := NewLANRelay(t)
	mac, gpu := newFreshMachine(t, r, "mac"), newFreshMachine(t, r, "gpu-box")

	mac.prompt.steps = []promptStep{
		askLine("Relay URL", r.URL()),
		askPassword("Relay admin token", AdminToken),
		askLine("Add cravv-connect to claude?", ""),
		askLine("Pair a device now?", ""),
		askPassword("Login password", Password),
		askLine("Local name for this peer", ""),
	}
	gpu.prompt.steps = []promptStep{
		askLine("Join relay "+r.URL()+"?", "y"),
		askPassword("Login password", Password),
		askLine("Local name for this peer", ""),
		askLine("Add cravv-connect to claude?", "y"),
	}
	macDone := make(chan int, 1)
	go func() { macDone <- mac.Run("setup") }()
	code := mac.JoinCode()
	if got := gpu.Run("setup", "--join", code); got != 0 {
		t.Fatalf("setup --join exited %d:\n%s", got, gpu.out.String())
	}
	select {
	case got := <-macDone:
		if got != 0 {
			t.Fatalf("setup exited %d:\n%s", got, mac.out.String())
		}
	case <-time.After(wait):
		t.Fatalf("setup on the Mac did not finish:\n%s", mac.out.String())
	}

	for _, m := range []*freshMachine{mac, gpu} {
		out := m.out.String()
		if left := m.prompt.left(); len(left) != 0 {
			t.Errorf("%s never asked %+v; it asked %q", m.name, left, m.prompt.asked)
		}
		for _, want := range []string{"Setup is complete.", "Installed cravv-connect for claude.", "codex: not found on this machine, skipped."} {
			if !strings.Contains(out, want) {
				t.Errorf("%s's setup output lacks %q:\n%s", m.name, want, out)
			}
		}
		if strings.Contains(out, "\u2014") {
			t.Errorf("%s's setup output has an em dash", m.name)
		}
		// Claude Code has the MCP server, the hooks, the allow rules and
		// the /cravv skill.
		if want := "claude mcp add --scope user cravv-connect -- " + installedBinary + " mcp"; len(m.claude.runs) != 1 || m.claude.runs[0] != want {
			t.Errorf("%s ran %q, want %q", m.name, m.claude.runs, want)
		}
		settings, err := os.ReadFile(filepath.Join(m.home, ".claude", "settings.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{installedBinary + " hook", `"mcp__cravv-connect__check_inbox"`, `"Bash(cravv-connect listen:*)"`} {
			if !strings.Contains(string(settings), want) {
				t.Errorf("%s's Claude settings lack %s:\n%s", m.name, want, settings)
			}
		}
		if _, err := os.Stat(filepath.Join(m.home, ".claude", "skills", "cravv", "SKILL.md")); err != nil {
			t.Errorf("%s has no /cravv skill: %v", m.name, err)
		}
	}

	// Paired: each daemon is on the relay and knows the other by name.
	macNode, gpuNode := mac.Node(), gpu.Node()
	for _, c := range []struct {
		n    *Node
		peer string
	}{{macNode, "gpu-box"}, {gpuNode, "mac"}} {
		st := c.n.Status()
		if !st.RelayConnected || len(st.Peers) != 1 || st.Peers[0].Alias != c.peer {
			t.Fatalf("%s after setup: %+v", c.n.Name, st)
		}
	}

	// Chat-connected: a chat on each machine, through the MCP server setup
	// registered, shares, links and talks.
	human := &Human{}
	trainer, _ := newClaudeAgent(t, gpuNode, "chat-trainer", human)
	lead, _ := newClaudeAgent(t, macNode, "chat-lead", nil)
	shareChat(t, trainer, "trainer", "all-peers")
	shareChat(t, lead, "lead", "private")
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "gpu-box/trainer", "permission": "messages"}, &out)
	gpuNode.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
	human.Answer(Choose("accept"))
	trainer.call("review_pending", nil)
	macNode.WaitLink(wait, "linked", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
	lead.call("send_message", map[string]any{"link": out.Link, "text": "ACC6-HELLO"})
	Eventually(t, wait, "the message in the GPU box's chat", func() bool {
		return strings.Contains(trainer.call("check_inbox", nil), "ACC6-HELLO")
	})
}
```

- [ ] **Step 3: Run it**

```sh
go test ./e2e -run 'TestAcceptance_6' -race -count=1 -v 2>&1 | grep -E '^(---|ok|FAIL)'
```

Expected: `--- PASS: TestAcceptance_6_Setup` and `ok`. Setup on the Mac asks, in order, for the relay URL, the admin token, Claude Code, pairing, the login password and the GPU box's name; setup on the GPU box asks to join the relay, for the password, the Mac's name and Claude Code. Any other question fails the run.

- [ ] **Step 4: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 5: Commit**

```sh
git add e2e/acceptance_setup_test.go e2e/harness.go
git commit -F - <<'MSG'
e2e: acceptance criterion 6: setup and setup --join take two fresh machines to paired and chat-connected against a real relay

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 4: e2e: acceptance criteria 7 and 8 (devices, sessions, links and rules from the chat and the web UI; password tiers, cut-offs, a blind relay, session identity)

Criterion 7 drives the chat's MCP tools and the web UI over HTTP on one pair: the page pauses and resumes a device and the chat sees it, the page sets and removes an offer with the password and the chat sees it in `sessions`, the chat connects and the page accepts at `tasks-auto` with the password, and both restrict and disconnect. Devices and rules are changed only from the page, as spec 7.3 and 10 put them behind the human. Criterion 8 has four subtests: the password tiers, the cut-offs (kill switch, pause, unpair), the relay's view of a whole v2 exchange (the recording backend of `relayblind_test.go`), and session identity against another local connection.

**Files:**
- Create: `e2e/acceptance_control_test.go`
- Test: the new file is the test

**Interfaces:**

Consumes: `OpenUI`, `UIBrowser.Get/Submit`, `wantPage`, `recordingBackend`, `NewRelayWith`, `Node.Unlocked`, `Unlock`, `Node.PeerView`, Task 1's `shareChat` and `taskState`.

Produces: `func offerFolder(t *testing.T) string`, `TestAcceptance_7_UI`, `TestAcceptance_8_SecurityHolds`.

- [ ] **Step 1: Write the acceptance tests for criteria 7 and 8**

Create `e2e/acceptance_control_test.go`:

```go
package e2e

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/relayserver"
)

// offerFolder returns a folder an offer may name (absolute, symlinks
// resolved, not the home folder).
func offerFolder(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAcceptance_7_UI pins criterion 7, "UI": "Devices, sessions, links
// and managed-session rules can be seen and changed both from the chat and
// from a local web page."
//
// From the chat (the MCP tools) the agent sees paired devices, the
// sessions and offers a device shows, and its own links, and changes its
// links (connect, restrict, disconnect). Changing a device or a rule is
// for the human (spec 7.3 and 10), so the chat sees those changes but
// makes them on the web page, which shows and changes all four.
func TestAcceptance_7_UI(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	lead, _ := newClaudeAgent(t, mac, "chat-lead", nil)
	shareChat(t, lead, "lead", "private")
	gpu.Share("claude", "trainer", "all-peers")
	page := OpenUI(t, gpu)

	// Devices: the chat sees them; the page pauses and resumes one.
	if text := lead.call("machines", nil); !strings.Contains(text, `"alias": "bob"`) || !strings.Contains(text, `"paused_by_peer": false`) {
		t.Fatalf("machines %s", text)
	}
	wantPage(t, page.Get("/devices"), "<td>alice</td>")
	wantPage(t, page.Submit("/devices", "/devices/pause", url.Values{"alias": {"alice"}}), "Paused alice.")
	Eventually(t, wait, "the chat sees the pause", func() bool {
		return strings.Contains(lead.call("machines", nil), `"paused_by_peer": true`)
	})
	wantPage(t, page.Submit("/devices", "/devices/resume", url.Values{"alias": {"alice"}}), "Resumed alice.")
	Eventually(t, wait, "the chat sees the resume", func() bool {
		return strings.Contains(lead.call("machines", nil), `"paused_by_peer": false`)
	})

	// Managed-session rules: set on the page (with the password), seen
	// from the chat as the device's offers.
	folder := offerFolder(t)
	rule := url.Values{"machine": {"alice"}, "label": {"acc7-rule"}, "folder": {folder}, "permission": {"messages"}, "run_mode": {"read-only"}}
	wantPage(t, page.Submit("/managed", "/managed/offers/set", rule), "This needs your login password.")
	rule.Set("password", Password)
	wantPage(t, page.Submit("/managed", "/managed/offers/set", rule), "Offer acc7-rule to alice: "+folder+" (read-only, messages).")
	wantPage(t, page.Get("/managed"), "<td>alice</td><td>acc7-rule</td>")
	var listed ipc.SessionsListResult
	lead.decode("sessions", map[string]any{"machine": "bob"}, &listed)
	if len(listed.Sessions) != 1 || listed.Sessions[0].Name != "trainer" || len(listed.Offers) != 1 || listed.Offers[0].Label != "acc7-rule" {
		t.Fatalf("the chat sees %+v", listed)
	}
	remove := url.Values{"machine": {"alice"}, "label": {"acc7-rule"}, "password": {Password}}
	wantPage(t, page.Submit("/managed", "/managed/offers/remove", remove), "Removed offer acc7-rule to alice")
	var after ipc.SessionsListResult
	lead.decode("sessions", map[string]any{"machine": "bob"}, &after)
	if len(after.Offers) != 0 {
		t.Fatalf("the chat still sees offers %+v", after.Offers)
	}

	// Sessions and links: the chat connects, the page accepts (tasks-auto
	// needs the password) and both see and change the link.
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-auto", "note": "ACC7"}, &out)
	in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	num := strconv.FormatInt(in.Link, 10)
	wantPage(t, page.Get("/approvals"), "<strong>alice/lead</strong> asks to link with your session <strong>trainer</strong>")
	wantPage(t, page.Submit("/approvals", "/approvals/link/accept", url.Values{"link": {num}, "password": {Password}}),
		"Accepted link "+num+": alice/lead may now use tasks-auto.")
	Eventually(t, wait, "the chat sees the link", func() bool {
		text := lead.call("links", nil)
		return strings.Contains(text, `"state": "active"`) && strings.Contains(text, `"permission_out": "tasks-auto"`)
	})
	wantPage(t, page.Get("/sessions"), "<td>trainer</td>", "alice/lead", "<td>active</td>")
	wantPage(t, page.Submit("/sessions", "/sessions/restrict", url.Values{"link": {num}, "permission": {"tasks-ask"}}), "Link "+num+" now allows tasks-ask.")
	Eventually(t, wait, "the chat sees the lower permission", func() bool {
		return strings.Contains(lead.call("links", nil), `"permission_out": "tasks-ask"`)
	})
	if text := lead.call("restrict", map[string]any{"link": out.Link, "permission": "messages"}); text != "Link "+strconv.FormatInt(out.Link, 10)+" now allows messages." {
		t.Fatalf("restrict from the chat %q", text)
	}
	gpu.WaitLink(wait, "the page's machine sees the chat's restrict", func(l ipc.LinkView) bool { return l.Link == in.Link && l.PermissionOut == "messages" })
	lead.call("disconnect", map[string]any{"link": out.Link})
	gpu.WaitLink(wait, "the page's machine sees the disconnect", func(l ipc.LinkView) bool { return l.Link == in.Link && l.State == "closed" })
	wantPage(t, page.Get("/sessions"), "<td>closed: closed_by_peer</td>")
}

// TestAcceptance_8_SecurityHolds pins criterion 8, "Security": "Pairing,
// broad grants and managed-session rules keep the v1 password gate.", "The
// kill switch, pause and unpair keep working.", "The relay stays blind."
// and "Session identity cannot be taken over by local processes that were
// not given it."
func TestAcceptance_8_SecurityHolds(t *testing.T) {
	t.Parallel()

	t.Run("pairing, broad grants and rules need the password", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		trainer := gpu.Share("claude", "trainer", "all-peers")
		lead := mac.Share("claude", "lead", "private")
		plain := gpu.Conn()
		wantKind(t, TryCall(plain, ipc.MethodPairStart, nil, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodJoinStart, ipc.JoinStartParams{Code: "CRAVV-0000-0000-0000"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "alice", Label: "x", Folder: offerFolder(t), Permission: "messages"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodOffersRemove, ipc.OfferRemoveParams{Machine: "alice", Label: "x"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "wrong"}, nil), ipc.KindBadPassword)

		Connect(t, lead, "bob/trainer", "tasks-auto", "")
		in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
		// Accepting at tasks-auto: not from the chat, not without the password.
		wantKind(t, TryCall(trainer.C, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: "link-" + strconv.FormatInt(in.Link, 10), Accept: true}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: in.Link, Accept: true}, nil), ipc.KindAuthRequired)
		// Accepting lower from the terminal, then raising, needs it too.
		gpu.Decide(in.Link, true, "messages")
		wantKind(t, TryCall(trainer.C, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: in.Link, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: in.Link, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
		if l := gpu.Link(in.Link); l.PermissionIn != "messages" {
			t.Fatalf("raised without the password: %+v", l)
		}
		// An agent's connection never edits rules, even unlocked: only the
		// owner's CLI or web UI may.
		agent := trainer.C
		Unlock(t, agent)
		wantKind(t, TryCall(agent, ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "alice", Label: "x", Folder: offerFolder(t), Permission: "messages"}, nil), ipc.KindBadRequest)
	})

	t.Run("kill switch, pause and unpair cut links off", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		killed := LinkUp(t, mac, gpu, "tasks-auto")
		Call(t, killed.B.C, ipc.MethodKill, nil, nil) // the agent's own kill_switch works
		wantKind(t, TryCall(killed.B.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: killed.BNum, Text: "x"}, nil), ipc.KindKilled)
		mac.WaitLink(wait, "killed", func(l ipc.LinkView) bool {
			return l.Link == killed.ANum && l.State == "closed" && l.Reason == core.CloseKilled
		})
		wantKind(t, TryCall(gpu.Conn(), ipc.MethodResume, nil, nil), ipc.KindAuthRequired)
		Call(t, gpu.Unlocked(), ipc.MethodResume, nil, nil)
		if l := gpu.Link(killed.BNum); l.State != "closed" {
			t.Fatalf("a link came back after resume: %+v", l)
		}

		paused := LinkChats(t, mac, gpu, mac.Share("claude", "p-lead", "private"), gpu.Share("claude", "p-trainer", "all-peers"), "messages")
		Call(t, mac.Conn(), ipc.MethodPeerPause, ipc.AliasParams{Alias: "bob"}, nil)
		gpu.WaitLink(wait, "paused", func(l ipc.LinkView) bool {
			return l.Link == paused.BNum && l.State == "closed" && l.Reason == core.ClosePaused
		})
		wantKind(t, TryCall(paused.A.C, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "bob/p-trainer", Permission: "messages"}, nil), ipc.KindPaused)
		Call(t, mac.Conn(), ipc.MethodPeerResume, ipc.AliasParams{Alias: "bob"}, nil)

		unpaired := LinkChats(t, mac, gpu, mac.Share("claude", "u-lead", "private"), gpu.Share("claude", "u-trainer", "all-peers"), "messages")
		Call(t, gpu.Conn(), ipc.MethodPeerUnpair, ipc.AliasParams{Alias: "alice"}, nil)
		mac.WaitLink(wait, "unpaired", func(l ipc.LinkView) bool {
			return l.Link == unpaired.ANum && l.State == "closed" && l.Reason == core.CloseUnpaired
		})
		Eventually(t, wait, "both forget each other", func() bool {
			_, a := mac.PeerView("bob")
			_, b := gpu.PeerView("alice")
			return !a && !b
		})
	})

	t.Run("the relay stays blind", func(t *testing.T) {
		t.Parallel()
		rec := &recordingBackend{}
		r := NewRelayWith(t, func(b relayserver.Backend) relayserver.Backend { rec.Backend = b; return rec })
		mac := NewNode(t, r, "acc8-mac-q1", NodeOptions{AdminToken: AdminToken})
		gpu := NewNode(t, r, "acc8-gpu-q2", NodeOptions{})
		Pair(t, mac, gpu)
		const (
			purpose = "ACC8-PURPOSE-3c1f"
			note    = "ACC8-NOTE-8d2e"
			label   = "acc8-offer-5b7a"
			chat    = "ACC8-CHAT-1a9c"
			instr   = "ACC8-TASK-6e4d"
			result  = "ACC8-RESULT-2f0b"
		)
		Call(t, gpu.Unlocked(), ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "acc8-mac-q1", Label: label, Folder: offerFolder(t), Permission: "messages"}, nil)
		c, _ := gpu.Session("claude")
		var shared ipc.ShareResult
		Call(t, c, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "acc8-trainer-z9", Purpose: purpose, Visibility: "all-peers"}, &shared)
		trainer := &SharedChat{C: c, Name: "acc8-trainer-z9", Res: shared}
		lead := mac.Share("claude", "acc8-lead-y8", "private")
		var listed ipc.SessionsListResult
		Call(t, lead.C, ipc.MethodSessionsList, ipc.MachineParams{Machine: "acc8-gpu-q2"}, &listed)
		if len(listed.Sessions) != 1 || len(listed.Offers) != 1 {
			t.Fatalf("discovery %+v", listed)
		}
		out := Connect(t, lead, "acc8-gpu-q2/acc8-trainer-z9", "tasks-auto", note)
		in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
		gpu.Decide(in.Link, true, "")
		mac.WaitLink(wait, "active", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
		id := sendChat(t, lead.C, out.Link, chat)
		WaitItem(t, trainer.C, wait, "the chat", isChat(id))
		var task ipc.TaskCreateResult
		Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: out.Link, Instructions: instr}, &task)
		WaitItem(t, trainer.C, wait, "the task", func(it ipc.InboxView) bool { return it.TaskID == task.TaskID })
		Call(t, trainer.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: task.TaskID}, nil)
		Call(t, trainer.C, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: task.TaskID, Result: result}, nil)
		taskState(t, lead.C, task.TaskID, "done")
		Call(t, lead.C, ipc.MethodSessionClose, nil, nil)
		gpu.WaitLink(wait, "closed", func(l ipc.LinkView) bool { return l.Link == in.Link && l.State == "closed" })

		frames, chunks := rec.snapshot()
		if len(frames) < 8 {
			t.Fatalf("recorded %d frames: the recorder missed traffic", len(frames))
		}
		secrets := []string{purpose, note, label, chat, instr, result, "acc8-mac-q1", "acc8-gpu-q2", "acc8-trainer-z9", "acc8-lead-y8",
			shared.WakeToken, shared.ReattachToken}
		for _, blobs := range [][][]byte{frames, chunks} {
			for i, data := range blobs {
				for _, s := range secrets {
					if bytes.Contains(data, []byte(s)) {
						t.Errorf("the relay saw %q in stored item %d", s, i)
					}
				}
			}
		}
	})

	t.Run("session identity cannot be taken over", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		trainer, _ := newClaudeAgent(t, gpu, "chat-trainer", nil)
		listener := shareChat(t, trainer, "trainer", "all-peers")
		lead := mac.Share("claude", "lead", "private")
		Connect(t, lead, "bob/trainer", "messages", "")
		in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
		gpu.Decide(in.Link, true, "")

		// The listener's command line names a file, never a token; the
		// file is the user's alone.
		file := strings.TrimPrefix(listener, "cravv-connect listen --wake-file ")
		token, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 || strings.Contains(listener, strings.TrimSpace(string(token))) {
			t.Fatalf("wake file %s: %v %v", file, fi, err)
		}

		// Another local process of the same user, on its own connection:
		// no session (not_shared) and no link of the chat's.
		other, _ := gpu.Session("claude")
		wantKind(t, TryCall(other, ipc.MethodInboxCheck, ipc.InboxCheckParams{}, nil), ipc.KindNotShared)
		wantKind(t, TryCall(other, ipc.MethodChatSend, ipc.ChatSendParams{Link: in.Link, Text: "impostor"}, nil), ipc.KindNotShared)
		wantKind(t, TryCall(other, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: in.Link}, nil), ipc.KindNotShared)
		// Guessing or reusing tokens: the wake token only counts, and is
		// no reattach token.
		for _, guess := range []string{strings.TrimSpace(string(token)), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
			wantKind(t, TryCall(other, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: guess}, nil), ipc.KindNotFound)
		}
		var counts json.RawMessage
		Call(t, gpu.Conn(), ipc.MethodSessionListen, ipc.SessionListenParams{WakeToken: strings.TrimSpace(string(token)), TimeoutS: 1}, &counts)
		var keys map[string]any
		if err := json.Unmarshal(counts, &keys); err != nil {
			t.Fatal(err)
		}
		for k := range keys {
			if k != "unread" && k != "requests" && k != "approvals" && k != "pending" && k != "closed" {
				t.Fatalf("the wake token revealed %q: %s", k, counts)
			}
		}
		if l := gpu.Link(in.Link); l.State != "active" {
			t.Fatalf("the link changed: %+v", l)
		}
		trainer.call("links", nil) // the chat still holds its session
	})
}
```

- [ ] **Step 2: Run them**

```sh
go test ./e2e -run 'TestAcceptance_[78]' -race -count=1 -v 2>&1 | grep -E '^(---|    ---|ok|FAIL)'
```

Expected: `--- PASS: TestAcceptance_7_UI`, `--- PASS: TestAcceptance_8_SecurityHolds` with its four subtests, `ok`.

- [ ] **Step 3: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 4: Commit**

```sh
git add e2e/acceptance_control_test.go
git commit -F - <<'MSG'
e2e: acceptance criteria 7 and 8: devices, sessions, links and rules from the chat and the web UI; password tiers, cut-offs, a blind relay, session identity

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 5: e2e: the Mac and GPU box scenario (two links, no cross-talk, closing gpu-box/trainer tells only mac/training)

The user's setup as one test: the Mac shares `training` and `voice`, the GPU box `trainer` and `wakeword`; `mac/training <-> gpu-box/trainer` is accepted at `tasks-auto` with the GPU box's password and `mac/voice <-> gpu-box/wakeword` at `tasks-ask` in the wakeword chat's form. A message, a task and a reply flow on each link. Every tool result each of the four chats' models saw is recorded, and none may contain the other link's markers. Finally three listener processes wait while `gpu-box/trainer` closes: only `mac/training`'s wakes.

**Files:**
- Create: `e2e/scenario_test.go`
- Test: the new file is the test

**Interfaces:**

Consumes: `newClaudeAgent` (its second result records what the model saw), `Human`, `Choose`, Task 1's `shareChat` and `Node.ListenerProcess`, `Waited`, `Silent`.

Produces: `scenarioChat`, `newScenarioChat`, `scenarioChat.sawOnly`, `TestScenario_MacAndGPUBox`.

- [ ] **Step 1: Write the scenario test**

Create `e2e/scenario_test.go`:

```go
package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// scenarioChat is one of the four chats of the scenario.
type scenarioChat struct {
	m        *mcpAgent
	seen     *[]string // every tool result this chat's model saw
	listener string
}

func newScenarioChat(t *testing.T, n *Node, name, visibility string, h *Human) *scenarioChat {
	t.Helper()
	m, seen := newClaudeAgent(t, n, "chat-"+name, h)
	return &scenarioChat{m: m, seen: seen, listener: shareChat(t, m, name, visibility)}
}

// sawOnly fails if the chat's model saw any marker of another link.
func (c *scenarioChat) sawOnly(t *testing.T, name string, others ...string) {
	t.Helper()
	for _, s := range *c.seen {
		for _, o := range others {
			if strings.Contains(s, o) {
				t.Errorf("%s saw %q: %s", name, o, s)
			}
		}
	}
}

// The user's setup: the Mac shares two chats (training and voice), the GPU
// box shares two (trainer and wakeword), and the links are
// mac/training <-> gpu-box/trainer and mac/voice <-> gpu-box/wakeword.
// Messages and tasks flow on each link, neither pair of chats sees the
// other's traffic, and closing gpu-box/trainer tells only mac/training.
func TestScenario_MacAndGPUBox(t *testing.T) {
	t.Parallel()
	r := NewRelay(t)
	mac := NewNode(t, r, "mac", NodeOptions{AdminToken: AdminToken})
	gpu := NewNode(t, r, "gpu-box", NodeOptions{})
	Pair(t, mac, gpu)
	trainerHuman, wakewordHuman := &Human{}, &Human{}
	training := newScenarioChat(t, mac, "training", "private", nil)
	voice := newScenarioChat(t, mac, "voice", "private", nil)
	trainer := newScenarioChat(t, gpu, "trainer", "all-peers", trainerHuman)
	wakeword := newScenarioChat(t, gpu, "wakeword", "all-peers", wakewordHuman)

	// training <-> trainer at tasks-auto: the GPU box's human grants it
	// with the password. voice <-> wakeword at tasks-ask: decided in the
	// wakeword chat's form.
	var trainLink, voiceLink ipc.LinkView
	training.m.decode("connect", map[string]any{"target": "gpu-box/trainer", "permission": "tasks-auto", "note": "SCN-TRAIN-NOTE"}, &trainLink)
	voice.m.decode("connect", map[string]any{"target": "gpu-box/wakeword", "permission": "tasks-ask", "note": "SCN-VOICE-NOTE"}, &voiceLink)
	trainIn := gpu.WaitLink(wait, "training's request", func(l ipc.LinkView) bool { return l.Session == "trainer" && l.State == "pending" })
	voiceIn := gpu.WaitLink(wait, "voice's request", func(l ipc.LinkView) bool { return l.Session == "wakeword" && l.State == "pending" })
	gpu.Decide(trainIn.Link, true, "")
	wakewordHuman.Answer(Choose("accept"))
	if text := wakeword.m.call("review_pending", nil); !strings.Contains(text, "accepted") {
		t.Fatalf("wakeword's review %q", text)
	}
	if text := trainer.m.call("review_pending", nil); text != "Nothing is waiting for a decision." {
		t.Fatalf("trainer was asked about another chat's request: %q", text)
	}
	mac.WaitLink(wait, "training linked", func(l ipc.LinkView) bool { return l.Link == trainLink.Link && l.State == "active" })
	mac.WaitLink(wait, "voice linked", func(l ipc.LinkView) bool { return l.Link == voiceLink.Link && l.State == "active" })
	for _, c := range []*scenarioChat{training, voice, trainer, wakeword} {
		c.m.call("check_inbox", nil)
	}

	// Traffic on each link.
	training.m.call("send_message", map[string]any{"link": trainLink.Link, "text": "SCN-TRAIN-MSG"})
	voice.m.call("send_message", map[string]any{"link": voiceLink.Link, "text": "SCN-VOICE-MSG"})
	var trainTask, voiceTask ipc.TaskCreateResult
	training.m.decode("create_task", map[string]any{"link": trainLink.Link, "instructions": "SCN-TRAIN-TASK run epoch 1"}, &trainTask)
	voice.m.decode("create_task", map[string]any{"link": voiceLink.Link, "instructions": "SCN-VOICE-TASK record samples"}, &voiceTask)

	// trainer works on its task at once (tasks-auto).
	Eventually(t, wait, "trainer gets its message and task", func() bool {
		trainer.m.call("check_inbox", nil)
		all := strings.Join(*trainer.seen, "\n")
		return strings.Contains(all, "SCN-TRAIN-MSG") && strings.Contains(all, "SCN-TRAIN-TASK")
	})
	trainer.m.call("claim_task", map[string]any{"task_id": trainTask.TaskID})
	trainer.m.call("complete_task", map[string]any{"task_id": trainTask.TaskID, "result": "SCN-TRAIN-RESULT"})
	trainer.m.call("send_message", map[string]any{"link": trainIn.Link, "text": "SCN-TRAINER-REPLY"})

	// wakeword's task waits for its human, who approves it in the form.
	Eventually(t, wait, "wakeword gets its message", func() bool {
		wakeword.m.call("check_inbox", nil)
		return strings.Contains(strings.Join(*wakeword.seen, "\n"), "SCN-VOICE-MSG")
	})
	wakewordHuman.Answer(Choose("accept"))
	Eventually(t, wait, "wakeword's human approves the task", func() bool {
		return strings.Contains(wakeword.m.call("review_pending", nil), "approved by the human")
	})
	if !strings.Contains(wakeword.m.call("check_inbox", nil), "SCN-VOICE-TASK") {
		t.Fatal("the approved task did not reach wakeword")
	}
	wakeword.m.call("claim_task", map[string]any{"task_id": voiceTask.TaskID})
	wakeword.m.call("complete_task", map[string]any{"task_id": voiceTask.TaskID, "result": "SCN-VOICE-RESULT"})
	wakeword.m.call("send_message", map[string]any{"link": voiceIn.Link, "text": "SCN-WAKEWORD-REPLY"})

	// Each Mac chat gets its own link's result and reply.
	for _, c := range []struct {
		chat  *scenarioChat
		task  string
		reply string
	}{{training, trainTask.TaskID, "SCN-TRAINER-REPLY"}, {voice, voiceTask.TaskID, "SCN-WAKEWORD-REPLY"}} {
		Eventually(t, wait, "the result and the reply", func() bool {
			c.chat.m.call("check_inbox", nil)
			done := strings.Contains(c.chat.m.call("get_task", map[string]any{"task_id": c.task}), `"state": "done"`)
			return done && strings.Contains(strings.Join(*c.chat.seen, "\n"), c.reply)
		})
	}

	// Neither pair saw the other's traffic, and cannot reach it.
	voiceMarks := []string{"SCN-VOICE", "SCN-WAKEWORD"}
	trainMarks := []string{"SCN-TRAIN", "SCN-TRAINER"}
	training.sawOnly(t, "mac/training", voiceMarks...)
	trainer.sawOnly(t, "gpu-box/trainer", voiceMarks...)
	voice.sawOnly(t, "mac/voice", trainMarks...)
	wakeword.sawOnly(t, "gpu-box/wakeword", trainMarks...)
	for _, c := range []struct {
		chat *scenarioChat
		task string
	}{{voice, trainTask.TaskID}, {wakeword, trainTask.TaskID}, {training, voiceTask.TaskID}, {trainer, voiceTask.TaskID}} {
		if text, isErr := c.chat.m.try("get_task", map[string]any{"task_id": c.task}); !isErr || !strings.Contains(text, "not found") {
			t.Fatalf("another link's task was visible: %q", text)
		}
	}

	// Closing gpu-box/trainer tells mac/training and nobody else.
	trainingWakes := mac.ListenerProcess(t, training.listener)
	voiceWakes := mac.ListenerProcess(t, voice.listener)
	wakewordWakes := gpu.ListenerProcess(t, wakeword.listener)
	Silent(t, trainingWakes, 300*time.Millisecond, "everything read")
	start := time.Now()
	trainer.m.call("session_close", nil)
	w := Waited(t, trainingWakes, 5*time.Second, "training learns the close")
	if want := fmt.Sprintf("cravv-connect: 1 link notice on link %d from gpu-box. Call check_inbox.\n", trainLink.Link); w.Stdout != want {
		t.Fatalf("training's listener %q, want %q", w.Stdout, want)
	}
	t.Logf("mac/training learned of the close after %s", time.Since(start).Round(time.Millisecond))
	if l := mac.Link(trainLink.Link); l.State != "closed" || l.Reason != core.CloseSessionClosed {
		t.Fatalf("training's link %+v", l)
	}
	Silent(t, voiceWakes, time.Second, "mac/voice is not told")
	Silent(t, wakewordWakes, 0, "gpu-box/wakeword is not told")
	if l := mac.Link(voiceLink.Link); l.State != "active" {
		t.Fatalf("voice's link %+v", l)
	}
	if !strings.Contains(training.m.call("check_inbox", nil), "closed") {
		t.Fatal("training's inbox does not say the link closed")
	}
	// The other pair keeps talking.
	voice.m.call("send_message", map[string]any{"link": voiceLink.Link, "text": "SCN-VOICE-AFTER"})
	Waited(t, wakewordWakes, wait, "wakeword still gets messages")
}
```

- [ ] **Step 2: Run it**

```sh
go test ./e2e -run 'TestScenario_MacAndGPUBox' -race -count=1 -v 2>&1 | grep -E 'learned|^(---|ok|FAIL)'
```

Expected: a log line `mac/training learned of the close after <about 1s>` (the listener is a real process), `--- PASS: TestScenario_MacAndGPUBox`, `ok`.

- [ ] **Step 3: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 4: Commit**

```sh
git add e2e/scenario_test.go
git commit -F - <<'MSG'
e2e: the Mac and GPU box scenario: two links, no cross-talk, closing gpu-box/trainer tells only mac/training

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 6: protocol: ipc-v1 for v2, with the method table generated from the daemon's registry

`protocol/ipc-v1.md` still described the v1 API (trust levels, machine-wide inboxes, the JSON agent CLI). It is rewritten for v2: the four kinds of connection (human, attachment and shared session, managed run, listener), the gate order, and every method with its params and result. Section 5's table is not written by hand: `TestIPCDocMatchesRegistry` builds the registry exactly as `app.Serve` does (`api.NewServer`, `RegisterUI`, `RegisterManaged` on a real daemon) and renders method, gate, while-killed and from-a-run for each; `CRAVV_UPDATE_DOCS=1` rewrites the block between the markers. It also parses every `Method*` constant in `internal/ipc` with `go/ast`, so a method registered somewhere the test does not build still fails, and it requires a method reference row (a table row whose first cell is the method in backticks) in section 6 for every method, and none for a method that does not exist.

**Files:**
- Create: `e2e/docs_ipc_test.go`
- Modify: `protocol/ipc-v1.md` (rewritten)
- Test: `e2e/docs_ipc_test.go`

**Interfaces:**

Consumes: `ipc.Server.Methods`, `ipc.Gate*`, `ipc.RunMethods`, `ipc.MethodCancel`, `api.NewServer`, `api.RegisterUI`, `api.RegisterManaged`, `app.Ports`, `app.UIPorts`, `app.ManagedPorts`, `NewNode`.

Produces: `TestIPCDocMatchesRegistry`; the generated table markers `<!-- ipc-methods: ... -->` and `<!-- /ipc-methods -->` in `protocol/ipc-v1.md`.

- [ ] **Step 1: Write the failing test**

Create `e2e/docs_ipc_test.go`:

```go
package e2e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/api"
	"github.com/cookwithcravv/cravv-connect/internal/app"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// ipcDoc is the local API's specification.
const ipcDoc = "../protocol/ipc-v1.md"

// The generated method table sits between these markers in ipcDoc.
const (
	ipcTableBegin = "<!-- ipc-methods: generated from the daemon's registry by TestIPCDocMatchesRegistry; CRAVV_UPDATE_DOCS=1 go test ./e2e -run TestIPCDocMatchesRegistry rewrites it -->"
	ipcTableEnd   = "<!-- /ipc-methods -->"
)

// ipcRegistry returns every method the daemon serves and its gates, from
// the registries app.Serve registers (api.NewServer, RegisterUI,
// RegisterManaged) on a real daemon.
func ipcRegistry(t *testing.T) map[string]ipc.Gate {
	t.Helper()
	n := NewNode(t, NewRelay(t), "docs", NodeOptions{})
	srv := api.NewServer(app.Ports(n.Daemon), n.Clock, nil)
	api.RegisterUI(srv, app.UIPorts(n.Daemon, nil))
	api.RegisterManaged(srv, app.ManagedPorts(n.Daemon))
	return srv.Methods()
}

// ipcMethodConstants returns the value of every Method* string constant in
// internal/ipc, so a method registered somewhere this test does not build
// still has to be documented.
func ipcMethodConstants(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("../internal/ipc/*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok {
				return true
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Method") || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					if v != ipc.MethodCancel { // a protocol notification, not a method
						out = append(out, v)
					}
				}
			}
			return true
		})
	}
	slices.Sort(out)
	return out
}

// gateWords says what a method's gates ask of the calling connection.
func gateWords(g ipc.Gate) string {
	var needs []string
	if g&ipc.GateSession != 0 {
		needs = append(needs, "registered")
	}
	if g&ipc.GateShared != 0 {
		needs = append(needs, "shared session")
	}
	if g&ipc.GateUnlock != 0 {
		needs = append(needs, "password")
	}
	if len(needs) == 0 {
		return "nothing"
	}
	return strings.Join(needs, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// ipcMethodTable renders the generated table for the registry.
func ipcMethodTable(methods map[string]ipc.Gate) string {
	names := make([]string, 0, len(methods))
	for m := range methods {
		names = append(names, m)
	}
	slices.Sort(names)
	var b strings.Builder
	b.WriteString(ipcTableBegin + "\n\n")
	b.WriteString("| Method | Gate | While killed | From a managed run |\n|---|---|---|---|\n")
	for _, m := range names {
		g := methods[m]
		b.WriteString("| `" + m + "` | " + gateWords(g) + " | " + yesNo(g&ipc.GateAllowWhenKilled != 0) + " | " + yesNo(ipc.RunMethods[m]) + " |\n")
	}
	b.WriteString("\n" + ipcTableEnd)
	return b.String()
}

var ipcTableBlock = regexp.MustCompile(`(?s)<!-- ipc-methods: .*?<!-- /ipc-methods -->`)

// The IPC spec lists every method the daemon serves, with the gates it
// really has (the generated table), and documents each one's params and
// result in the method reference (a row starting "| `method` |"). Nothing
// is documented that the daemon does not serve.
func TestIPCDocMatchesRegistry(t *testing.T) {
	t.Parallel()
	methods := ipcRegistry(t)
	for _, c := range ipcMethodConstants(t) {
		if _, ok := methods[c]; !ok {
			t.Errorf("ipc declares method %q, but the daemon does not register it", c)
		}
	}
	raw, err := os.ReadFile(ipcDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	want := ipcMethodTable(methods)
	if os.Getenv("CRAVV_UPDATE_DOCS") == "1" {
		if !ipcTableBlock.MatchString(doc) {
			t.Fatalf("%s has no generated method table to update", ipcDoc)
		}
		doc = ipcTableBlock.ReplaceAllLiteralString(doc, want)
		if err := os.WriteFile(ipcDoc, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := ipcTableBlock.FindString(doc); got != want {
		t.Errorf("the method table in %s is not what the registry says; run CRAVV_UPDATE_DOCS=1 go test ./e2e -run TestIPCDocMatchesRegistry\n got:\n%s\nwant:\n%s", ipcDoc, got, want)
	}

	ref := ipcTableBlock.ReplaceAllString(doc, "")
	start := strings.Index(ref, "\n## 6. Method reference")
	if start < 0 {
		t.Fatalf("%s has no section 6. Method reference", ipcDoc)
	}
	ref = ref[start+1:]
	if end := strings.Index(ref, "\n## 7."); end >= 0 {
		ref = ref[:end]
	}
	row := regexp.MustCompile("(?m)^\\| `([a-z_$/.]+)` \\|")
	documented := map[string]bool{}
	for _, m := range row.FindAllStringSubmatch(ref, -1) {
		documented[m[1]] = true
		if _, ok := methods[m[1]]; !ok {
			t.Errorf("the method reference documents %q, which the daemon does not serve", m[1])
		}
	}
	for m := range methods {
		if !documented[m] {
			t.Errorf("the method reference does not document %q", m)
		}
	}
}
```

```sh
go test ./e2e -run TestIPCDocMatchesRegistry -count=1 2>&1 | head -4
```

Expected: `FAIL` with `the method table in ../protocol/ipc-v1.md is not what the registry says` (the v1 document has no generated table).

- [ ] **Step 2: Rewrite the document**

Replace the whole of `protocol/ipc-v1.md` with:

````markdown
# ipc-v1: the local daemon API

Status: normative. Transport version 1, with the v2 methods for shared
sessions, links, decisions in chat, managed sessions and the web UI.

ipc-v1 is how local clients talk to `cravv-connect daemon`: the MCP server
(`cravv-connect mcp`), the listener (`cravv-connect listen`), the hook
(`cravv-connect hook`), every CLI command and the web UI (which runs inside
the daemon and uses in-process connections served exactly like socket
ones). Only the daemon touches the network.

Go reference: `internal/ipc` (transport, gates, errors, method names,
views), `internal/api` (one file per method group) and `internal/app`
(adapters and extra error kinds). Section 5's table is generated from the
daemon's registry and checked by `TestIPCDocMatchesRegistry` in `e2e/`.

## 1. Transport

- **Socket:** a unix stream socket at `$CRAVV_HOME/daemon.sock`
  (default `~/.cravv-connect/daemon.sock`). The directory is mode `0700` and the
  socket `0600`, so only the same OS user can connect. The daemon also reads
  the peer credentials of every accepted connection (`LOCAL_PEERCRED` on
  macOS, `SO_PEERCRED` on Linux) and closes it at once unless the connecting
  process runs as the daemon's UID.
- **Startup:** the daemon removes a stale socket left by a crash, refuses to
  replace anything that is not a socket, and refuses to start when another
  daemon answers on it.
- **Startup and stop:** the daemon writes `$CRAVV_HOME/daemon.pid` only after
  it owns the socket, and on exit removes it only if it still holds its own
  pid. `daemon.shutdown` stops it; `cravv-connect daemon stop` uses that and
  waits up to 10 seconds for the socket to disappear.
- **Framing:** newline-delimited JSON. Each line is one JSON-RPC 2.0 request or
  response, at most 8 MiB. The daemon writes JSON without HTML escaping
  (`<`, `>` and `&` stay one byte). A request line over the limit gets an
  error of kind `too_large` (id `null`) and the connection closes. A
  response that would be over the limit is never written: the daemon sends
  an error of kind `too_large` for that `id` instead, so the stream stays
  usable.
- **Concurrency:** a client may send several requests without waiting; each
  is handled concurrently and answered with its `id`. At most 32 requests
  may be running on one connection; more fail at once with kind `busy`.
  Requests without `id` are notifications and get no response.
- **Cancellation:** a client that stops waiting for a request sends the
  notification `{"jsonrpc":"2.0","method":"$/cancel","params":{"id":<id>}}`.
  The daemon cancels that request; its (error) response may still arrive and
  is ignored. A cancelled `inbox.check` or `inbox.wait` marks nothing read,
  so its items are returned by the next call.
- **Connection state:** a registration, a shared session, a managed run's
  binding, a password unlock and a held managed session (section 3) belong
  to one connection. Closing the connection ends them: a shared session
  goes away (section 3.2), a held managed session is released.

A client that cannot connect reports:

```text
daemon not running: run `cravv-connect daemon start`
```

## 2. Messages

Request:

```json
{"jsonrpc":"2.0","id":7,"method":"chat.send","params":{"link":2,"text":"hello"}}
```

Success:

```json
{"jsonrpc":"2.0","id":7,"result":{"id":"01J8ZQ9K7B2V4N6M8P0R2T4W6Y"}}
```

Error:

```json
{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"link 2 is closed","data":{"kind":"link_closed"}}}
```

- Missing, empty, or `null` params decode as the zero value. Methods that take
  nothing accept `{}`.
- A method that returns nothing returns `{}`.
- Error codes: `-32700` for a line that is not valid JSON or has no method,
  `-32601` for an unknown method, `-32602` for invalid params (kind
  `bad_request`), `-32000` for everything else. `data.kind` is always set.

## 3. Connections

A connection is one of four things, and what it may do follows from which.
Identity always comes from the connection, never from an argument: no
method takes a session ID, and no view returns one.

### 3.1 Human connections

A connection that never calls `session.register` is a human's: the CLI or
a web UI browser session. It acts on every link of the machine (`links`,
`link.decide`, `link.disconnect`, `link.restrict` are not limited to one
session), and it is the only kind that may call the owner's methods
(`offers.*`, `managed.*`, `ui.start`, `sessions.local`,
`link.connect_as`); an agent connection gets `bad_request` for those.

### 3.2 Attachments and shared sessions

`session.register` turns a connection into an agent attachment:

- The name is `<agent>@<basename of project_dir>`, with `-2`, `-3`, ... added
  when the name is taken, for example `claude@glow-v2` and `claude@glow-v2-2`.
  Each part is lowercased, limited to `a-z 0-9 . _ -` and at most 32
  characters. `agent` defaults to `agent`; `project_dir` must be absolute;
  `pid` is informational and ignored.
- A connection registers once. A disconnected attachment with the same
  agent and folder seen within 5 minutes is given back under its name.
- An attachment can use discovery and read-only methods but no link
  traffic: every link method gives it `not_shared` until it shares.

`session.share` shares the chat on a registered connection: a named
session other machines can link to, bound to this connection.

- `name`: 1 to 32 characters of `[a-z0-9-]`, not starting with `-`, unique
  among this machine's open and away sessions. `purpose`: one line, at most
  120 characters. `visibility`: `private` (the default), `all-peers`, or
  `peers:<alias>[,<alias>...]`.
- The agent and project folder come from the registration, never from the
  call. `agent_session` is the agent's own chat ID (Claude Code's session
  ID, which its hooks receive), so `hook.counts` finds this session.
- The result carries two secrets for this client only: the **wake token**
  (for `session.listen`: counts, nothing else) and the **reattach token**
  (for `session.reattach`). The daemon keeps only their SHA-256. Neither may
  be put on a command line.
- When the connection ends the session is **away**: its links stay open,
  peers see it away, and items for it wait. It closes (and all its links
  with it) on `session.close` or 10 minutes after it went away. A daemon
  restart ends every connection, so every live session starts away.
- `session.reattach` takes the session over with its reattach token, from
  a connection registered with the same agent and project folder; every
  failure (unknown token, other agent or folder, closed session) looks the
  same, `not_found`. The old connection loses the session at once
  (`not_shared` on its next call).

### 3.3 Managed runs

A managed run's `cravv-connect mcp` binds its connection with
`session.run_bind` and the run token from its environment
(`CRAVV_RUN_TOKEN`). The token is one run's and one connection's: the
first connection that binds it keeps it, and it is revoked when the run
ends. A bound connection may call only the methods marked "From a managed
run" in section 5 (its session's messages, tasks, files and link; not the
inbox, which the daemon hands the run in its prompt); every other method
fails with `not_permitted` before any gate. Files it sends are taken
relative to the managed session's folder.

The daemon also reads the peer PID of every socket connection. A
connection from a process inside a live run (in a run's process group, or
below the run's agent in the process tree) may call only
`session.register` and `session.run_bind` until it binds; anything else
fails with `not_permitted`. This is defense in depth: a process that left
its group and was reparented is not recognized.

### 3.4 The listener

`session.listen` needs no registration: the wake token is the whole
credential, and it reveals only counts (section 6.3). It blocks until the
session has something new (an inbox item after its read position), until
`timeout_s` passes (0 waits until the connection ends; at most 24 hours),
or until the session closes.

## 4. Gates

Each method has a gate, checked before the handler in this order:

1. **Managed run.** A connection bound by `session.run_bind` may call only
   the run methods, and an unbound connection from a process inside a run
   only `session.register` and `session.run_bind` (section 3.3); otherwise
   `not_permitted`.
2. **Kill switch.** While the kill switch is on, only the methods marked
   "While killed" run. Everything else fails with `killed`.
3. **registered:** `session.register` must have succeeded on this
   connection, otherwise `no_session`.
4. **shared session:** a session shared on this connection (or bound by a
   run token) that the daemon still binds to it, otherwise `not_shared`.
5. **password:** `auth.unlock` must have succeeded on this connection within
   the last 10 minutes, otherwise `auth_required`.

The daemon checks the password again inside every human-only action, so a
missing gate fails with `auth_required` instead of opening it: accepting a
link (`link.decide`, at any level), raising a link (`link.permit`), editing
offers (`offers.set`, `offers.remove`), deciding held tasks
(`approvals.decide`), `files.accept`, `resume`, `allow_path.add`,
`reset_identity` and the pairing methods. The chat tier (`review.decide`)
is the one other way to accept a link or approve a task: it needs a real
answer from the human's elicitation form or a confirmation code, and it
never grants `tasks-auto`.

`auth.unlock` checks the OS login password of the user running the daemon
with PAM: service `chkpasswd` on macOS and `login` on Linux by default, or
`pam_service` in `config.toml`, which must be one of `chkpasswd`, `checkpw`
(macOS) or `login`, `system-auth` (Linux); any other service fails every
unlock with `auth_unavailable`. The first time the daemon starts with a given
PAM service, and again after that service's `/etc/pam.d` file changes, it
checks that a random password is rejected, and refuses to start if it is
accepted; if PAM fails otherwise, it starts and `status` lists "password
check is not working" in `errors`. After 5 wrong passwords in a row, every
attempt fails with `locked` for 15 minutes without reaching PAM; the count
and the lock survive a daemon restart. Every non-empty attempt is written
to the audit log (never the password); an empty password fails with
`bad_password` without being counted. A binary built without cgo has no
PAM and fails every unlock with `auth_unavailable`.

## 5. Methods

"Gate" is what the connection needs (section 4): `registered`,
`shared session`, `password`, or `nothing`. "While killed" is yes when the
method runs while the kill switch is on. "From a managed run" is yes when a
run's connection may call it.

<!-- ipc-methods: generated from the daemon's registry by TestIPCDocMatchesRegistry; CRAVV_UPDATE_DOCS=1 go test ./e2e -run TestIPCDocMatchesRegistry rewrites it -->

| Method | Gate | While killed | From a managed run |
|---|---|---|---|
| `allow_path.add` | password | no | no |
| `approvals.decide` | password | no | no |
| `approvals.list` | password | no | no |
| `audit.read` | nothing | yes | no |
| `auth.unlock` | nothing | yes | no |
| `chat.send` | shared session | no | yes |
| `daemon.shutdown` | nothing | yes | no |
| `file.send` | shared session | no | yes |
| `files.accept` | password | no | no |
| `files.list` | nothing | no | no |
| `hook.counts` | nothing | yes | no |
| `inbox.check` | shared session | no | no |
| `inbox.wait` | shared session | no | no |
| `join.start` | password | no | no |
| `kill` | nothing | yes | no |
| `link.connect` | shared session | no | no |
| `link.connect_as` | password | no | no |
| `link.decide` | nothing | no | no |
| `link.disconnect` | nothing | no | no |
| `link.permit` | password | no | no |
| `link.restrict` | nothing | no | no |
| `links` | nothing | no | yes |
| `machines` | nothing | yes | no |
| `managed.close` | nothing | yes | no |
| `managed.list` | nothing | yes | no |
| `managed.open` | nothing | no | no |
| `offers.list` | nothing | yes | no |
| `offers.remove` | password | no | no |
| `offers.set` | password | no | no |
| `pair.await` | password | no | no |
| `pair.finalize` | password | no | no |
| `pair.start` | password | no | no |
| `peer.alias` | nothing | no | no |
| `peer.list` | nothing | yes | no |
| `peer.pause` | nothing | no | no |
| `peer.resume` | nothing | no | no |
| `peer.unpair` | nothing | no | no |
| `reset_identity` | password | yes | no |
| `resume` | password | yes | no |
| `review.code` | shared session | no | no |
| `review.decide` | shared session | no | no |
| `review.list` | shared session | no | no |
| `session.close` | shared session | no | no |
| `session.listen` | nothing | no | no |
| `session.reattach` | registered | no | no |
| `session.register` | nothing | no | yes |
| `session.run_bind` | registered | no | yes |
| `session.set` | shared session | no | no |
| `session.share` | registered | no | no |
| `sessions.list` | nothing | no | no |
| `sessions.local` | nothing | yes | no |
| `status` | nothing | yes | no |
| `task.cancel` | shared session | no | no |
| `task.claim` | shared session | no | yes |
| `task.complete` | shared session | no | yes |
| `task.create` | shared session | no | no |
| `task.fail` | shared session | no | yes |
| `task.get` | shared session | no | yes |
| `task.update` | shared session | no | yes |
| `ui.start` | nothing | yes | no |

<!-- /ipc-methods -->

## 6. Method reference

Sizes: `text`, `instructions`, `note`, `result` and `reason` are at most
65536 bytes (`too_large`). Required strings that are empty or only
whitespace fail with `bad_request`; `result` and `reason` may be empty.
`link` is always this machine's link number (as `links` shows it).

### 6.1 Connection and session

| Method | Params | Result |
|---|---|---|
| `session.register` | `{agent, project_dir, pid}` | `{name}` |
| `session.share` | `{name, purpose?, visibility?, agent_session?}` | `{session: SharedSessionView, wake_token, reattach_token}` |
| `session.reattach` | `{reattach_token, agent_session?}` | `SharedSessionView` |
| `session.close` | `{}` | `{}` |
| `session.set` | `{purpose?, visibility?}` | `SharedSessionView` |
| `session.listen` | `{wake_token, timeout_s?}` | `ListenResult` |
| `session.run_bind` | `{run_token}` | `SharedSessionView` |

- **`session.share`** fails with `bad_request` when this connection already
  shares an open session or the name is taken.
- **`session.close`** closes the session and every link it has
  (`link.closed{session_closed}` to each peer); pending requests to it are
  rejected. A listener waiting on the session exits with a line saying the
  session is closed.
- **`session.set`** changes the purpose or who can see the session; at
  least one field is required. Existing links stay.
- **`session.run_bind`** refuses a connection that already shares a chat
  (`bad_request`) and a token that is unknown, revoked or already bound by
  another connection (`not_found`).

### 6.2 Discovery and links

| Method | Params | Result |
|---|---|---|
| `machines` | `{}` | `{peers: [PeerView]}` |
| `sessions.list` | `{machine}` | `{machine, sessions: [RemoteSessionView], offers?: [RemoteOfferView]}` |
| `link.connect` | `{target, permission, note?}` | `LinkView` |
| `links` | `{}` | `{links: [LinkView]}` |
| `link.disconnect` | `{link}` | `{}` |
| `link.restrict` | `{link, permission}` | `LinkView` |
| `link.permit` | `{link, permission}` | `LinkView` |
| `link.decide` | `{link, accept, permission?}` | `LinkView` |

- **`sessions.list`** asks the machine and waits at most 10 seconds
  (`offline` otherwise). It returns only the open and away sessions that
  machine lets this one see, and the offers it makes to this one. The
  peer's free text (a purpose) is only in `wrapped`.
- **`link.connect`** asks `target` for a link from the session shared on
  this connection. `target` is `<machine>/<session>` (an open session the
  machine shows this one) or `<machine>/new:<label>` (a managed session
  from an offer). `permission` is what this side proposes to do there
  (`messages`, `tasks-ask` or `tasks-auto`); `note` is at most 280
  characters. The link is `pending` until the other side answers; a
  session the target does not show this machine is `not_found`. On this
  side the new link lets the peer send messages only (`permission_in` is
  `messages`); raising it is `link.permit`.
- **`links`**: a shared connection sees its own session's links, a human
  connection every link, an attachment gets `not_shared`.
- **`link.disconnect`** closes a link on both sides
  (`link.closed{closed_by_peer}` to the peer). Closed links never reopen.
- **`link.restrict`** lowers what the peer may do on this side
  (`tasks-auto` > `tasks-ask` > `messages`). No password; raising with it
  fails with `auth_required`. Lowering to `messages` rejects the peer's
  tasks still waiting on that link (for approval or for a claim).
- **`link.permit`** sets what the peer may do on this side, raising
  included. It always needs the password.
- **`link.decide`** accepts or rejects a pending request to this machine.
  Rejecting needs nothing; accepting needs the password at every level
  (a chat accepts through `review.decide`). `permission` grants less than
  asked; empty grants what was asked. A shared connection decides only its
  own session's requests; another session's looks missing.

### 6.3 Inbox and decisions in chat

| Method | Params | Result |
|---|---|---|
| `inbox.check` | `{limit?}` | `{items: [InboxView]}` |
| `inbox.wait` | `{timeout_s?}` | `{items: [InboxView]}` |
| `review.list` | `{}` | `{items: [ReviewItemView]}` |
| `review.decide` | `{item, accept, permission?, code?}` | `{item, outcome, link, permission?}` |
| `review.code` | `{item}` | `{}` |
| `hook.counts` | `{cwd, session_id?, event?, stop_hook_active?}` | `{notice, unread, approvals, block?, reason?}` |

- **`inbox.check`** returns the session's unread items (default 50, at
  most 200) and advances its read position past them and no further. A
  page also holds at most 4 MiB of `wrapped` text, always at least one
  item. The first time a queued task is returned, its sender is told it
  was `seen`.
- **`inbox.wait`** is `inbox.check` that blocks until an item arrives or
  `timeout_s` passes (0 or less is 50 seconds, at most 600). It returns
  `{"items": []}` on timeout.
- **`review.list`** is the session's pending decisions: link requests to it
  and its tasks awaiting approval. At most 6 calls a minute per session
  (`rate_limited`).
- **`review.decide`** applies the human's answer at the chat tier. Without
  `code` it is an answer from the human's elicitation form; with `code`,
  the 4-digit code the human typed from the desktop notification (needed
  to accept, not to reject). It accepts a link at `messages` or `tasks-ask`
  only (`tasks-auto` fails with `auth_required`; a code accepts at most
  `tasks-ask`), approves or denies one held task, or rejects. `outcome` is
  `accepted`, `rejected`, `approved` or `denied`. A wrong or expired code
  is `bad_code`; after 3 wrong codes for an item, or 10 for the session in
  24 hours, `code_locked`.
- **`review.code`** shows the item's code in a desktop notification; the
  code never travels over IPC. `no_desktop` when this machine shows no
  notifications (every machine but macOS).
- **`hook.counts`** answers a Claude Code hook for the chat `session_id`
  (or, when that is unknown, the newest open session of `cwd` no chat has
  claimed). `notice` is one line naming only local aliases, link numbers
  and counts. For `event` `Stop` it sets `block` with a one-line `reason`
  when the session has unhandled items (never for decisions alone), at
  most twice in a row and not again for the same items while
  `stop_hook_active`.

### 6.4 Messages, tasks and files

| Method | Params | Result |
|---|---|---|
| `chat.send` | `{link, text}` | `{id}` |
| `task.create` | `{link, instructions, file_paths?}` | `{task_id}` |
| `task.get` | `{task_id}` | `TaskView` |
| `task.claim` | `{task_id}` | `TaskView` |
| `task.update` | `{task_id, note}` | `TaskView` |
| `task.complete` | `{task_id, result, file_paths?}` | `TaskView` |
| `task.fail` | `{task_id, reason}` | `TaskView` |
| `task.cancel` | `{task_id}` | `TaskView` |
| `file.send` | `{link, path}` | `{file_id}` |
| `files.list` | `{}` | `{files: [FileView]}` |
| `files.accept` | `{file_id}` | `{}` |

- **Links.** Sends go on one of the session's own links, which must be
  `active`: any other state fails at once with `link_closed`; a link of
  another session is `not_found`.
- **`task.create`** fails with `not_permitted` when the peer lets this side
  send messages only. `file_paths` are sent first, under the rules of
  `file.send`.
- **`task.get`** returns an inbound task sent to this session that no human
  holds or rejected, or an outbound task this session created. Anything
  else, including another session's task, is `not_found`, so agents never
  read instructions no human approved.
- **`task.claim`** is atomic; `bad_transition` when the task is not
  `queued`. **`task.update`** (the claimer only) moves `claimed` to
  `running`. **`task.complete`** and **`task.fail`** are the claimer's,
  while `claimed` or `running`; `task.complete` marks the task done before
  sending `file_paths`, and a file that cannot be sent is returned as an
  error while the task stays done. **`task.cancel`** is the creator's, while
  the task is active.
- **`file.send`**: `path` is absolute or relative to the session's project
  folder. Refused with `path_refused`: paths outside the project folder and
  allowed folders, any component starting with `.`, names matching `.env*`,
  `id_*`, `credentials*.json`, `service-account*.json` or ending in `.pem`,
  `.key`, `.env`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.kdbx`, `.ppk`, `.ovpn`
  (case-insensitive), symlinks that resolve outside those folders,
  non-regular files, files with more than one hard link, and files over
  100 MiB.
- **`files.accept`** downloads a file held for a human. Links never hold
  files (every active link may carry files), so only files held before an
  upgrade from v1 can be accepted.

### 6.5 Machines, pairing and controls

| Method | Params | Result |
|---|---|---|
| `status` | `{}` | `StatusResult` |
| `peer.list` | `{}` | `{peers: [PeerView]}` |
| `peer.pause` | `{alias}` | `{}` |
| `peer.resume` | `{alias}` | `{}` |
| `peer.unpair` | `{alias}` | `{}` |
| `peer.alias` | `{alias, new_alias}` | `{}` |
| `auth.unlock` | `{password}` | `{expires_at}` |
| `pair.start` | `{}` | `{pending_id, code}` |
| `pair.await` | `{pending_id}` | `{pending_id, suggested_name, machine_id}` |
| `join.start` | `{code}` | `{pending_id, suggested_name, machine_id}` |
| `pair.finalize` | `{pending_id, alias}` | `{alias}` |
| `approvals.list` | `{}` | `{tasks: [ApprovalView]}` |
| `approvals.decide` | `{task_id, approve}` | `{}` |
| `allow_path.add` | `{path}` | `{}` |
| `kill` | `{}` | `{}` |
| `resume` | `{}` | `{}` |
| `reset_identity` | `{}` | `{}` |
| `audit.read` | `{limit}` | `{events: [Event]}` |
| `daemon.shutdown` | `{}` | `{}` |

- **`machines`** is `peer.list` under its v2 name.
- **`peer.pause`** stops traffic both ways and closes every link with the
  peer (`paused`); **`peer.unpair`** does the same (`unpaired`), then
  deletes the peer and its keys. Neither needs the password: cutting off
  stays easy. `peer.resume` allows new links again; closed links stay
  closed.
- **`peer.alias`**: `new_alias` is 1 to 24 characters of `a-z 0-9 -`,
  starting with a letter or digit.
- **Pairing.** `pair.start` needs a live relay connection (`offline`
  otherwise); `pair.await` blocks until the joiner finishes the exchange
  (at most 10 minutes); `pair.finalize` names the peer and, on a machine
  without a relay mailbox, registers one with the invite received while
  pairing. Pairing creates no links.
- **`approvals.list`** and **`approvals.decide`** are the password path for
  tasks held on `tasks-ask` links: oldest first, `preview` is the first 500
  characters, `sha256` the hex SHA-256 of the full text. Approving queues
  the task and delivers it to its session; it fails with `link_closed` when
  the task's link closed and `not_permitted` when the link no longer
  allows tasks. Denying rejects it and tells the sender.
- **`kill`** needs no password and persists across restarts. It stops
  managed runs, fails claimed and running tasks (`killed`), closes every
  link and tells the peers, tries for up to 3 seconds to send those
  updates, stops file transfers, and disconnects from the relay. The switch
  counts as on from the moment `kill` starts. Calling it again changes
  nothing. **`resume`** (password) turns it off; links must be requested
  again.
- **`reset_identity`** runs while killed: it turns the kill switch on (or
  leaves it on), deletes every peer, prekey and outbox item, stops pending
  pairings, and creates a new identity with no relay mailbox.
- **`audit.read`**: the newest `limit` events (default 50, at most 1000),
  oldest first.
- **`daemon.shutdown`** replies `{}` and the daemon exits about 100 ms
  later. A daemon run by launchd or systemd may be restarted by the
  service manager; `cravv-connect daemon stop` stops the service instead.

### 6.6 Managed sessions and the web UI

| Method | Params | Result |
|---|---|---|
| `offers.list` | `{machine?}` | `{offers: [OfferView]}` |
| `offers.set` | `{machine, label, folder, permission, run_mode?, shell_confirm?, agent?, max_concurrent?, idle_timeout_s?, max_turns_per_run?, run_timeout_s?, runs_per_hour?, runs_per_day?}` | `OfferView` |
| `offers.remove` | `{machine, label}` | `{}` |
| `managed.list` | `{}` | `{sessions: [ManagedView]}` |
| `managed.open` | `{name}` | `{name, machine, folder, command}` |
| `managed.close` | `{name}` | `{}` |
| `ui.start` | `{}` | `{url}` |
| `sessions.local` | `{}` | `{sessions: [SharedSessionView]}` |
| `link.connect_as` | `{session, target, permission, note?}` | `LinkView` |

All of these are for human connections only (section 3.1).

- **`offers.set`** creates or replaces the offer `label` to `machine`. A
  link to one of its managed sessions is granted the lower of what was
  asked and the offer's permission, and `tasks-ask` becomes `messages`
  (nobody could answer the ask). The
  folder must be absolute, exist, not be the home folder (also through a
  symlink) and neither contain nor be inside `~/.cravv-connect`; the path
  with symlinks resolved is stored and checked again before every run.
  `permission` is `messages` or `tasks-auto`; `run_mode` is `read-only`
  (default), `edit-in-folder` or `shell`, and `shell` needs
  `shell_confirm: "shell"`. Zero numbers take the defaults: 2 open
  sessions, 2 hours idle, 40 turns (recorded only), 30 minutes a run, 30
  runs an hour per link, 200 runs a day per machine. `agent` is `claude`.
- **`offers.remove`** removes the offer and closes its managed sessions.
- **`managed.open`** holds the session's queue for as long as this
  connection stays open, and returns the command that opens its
  conversation (`claude --resume <id>`) in its folder, and `machine`, the
  local alias of the machine whose peer drove it (the CLI and the web UI
  warn with it). Refused (`bad_transition`) before the session's first
  run.
- **`managed.close`** closes a managed session and its link; a cut-off, so
  no password, and it runs while killed.
- **`ui.start`** starts the web UI if it is not running and returns a URL
  with a new one-time launch token.
- **`link.connect_as`** is `link.connect` for the human, on behalf of the
  open local session named `session`. It needs the password, because the
  session's chat will receive what the other side sends.

## 7. Views

```jsonc
// InboxView
{"seq": 12, "id": "01J...", "from": "gpu-box", "session": "trainer", "link": 2,
 "kind": "chat",
 "wrapped": "<remote_message from=\"gpu-box\" session=\"trainer\" link=\"2\" ...>...</remote_message>",
 "at": "2026-09-26T10:00:00Z"}
```

- `kind` is `chat`, `task`, `task_update`, `file`, `link` (a request,
  acceptance, rejection or close) or `approval` (a task on this session
  waits for a human decision; it carries no instructions).
- `from` is the local alias and `link` the local link number. `session`
  is the peer's session name, cleaned like a wrapper attribute.
- `wrapped` is the only field agents should read as content:
  `<remote_message from="alias" session="..." link="N" permission="..." id="..." kind="..." task_id="...">body</remote_message>`
  with `session`, `link`, `permission` and `task_id` omitted when empty,
  attribute values stripped of control and invisible characters, capped at
  64 characters and XML-escaped, and a body on its own line with invisible
  characters removed and `&`, `<`, `>` escaped, so the body cannot close
  the tag. `permission` is what the link lets the peer do on this side.

```jsonc
// TaskView (an inbound task)
{"task_id": "01J...", "direction": "in", "peer": "gpu-box", "state": "running",
 "claimed_by": "trainer",
 "notes": [{"at": "2026-09-26T10:00:00Z", "text": "halfway"}],
 "files": [{"file_id": "01J...", "name": "", "size": 812}],
 "wrapped": "<remote_message from=\"gpu-box\" ... kind=\"task\" task_id=\"01J...\">\nInstructions:\n...\n</remote_message>",
 "updated_at": "2026-09-26T10:00:00Z"}
```

TaskView never returns text the peer wrote in a raw field. That text is
rendered once, with the same wrapper as InboxView, in `wrapped`: for an
inbound task the instructions and file names (`instructions` is omitted,
`files[].name` empty), for an outbound task the peer's result, its notes
and its result file names (`result` omitted, `notes` only this machine's,
`result_files[].name` empty). On the sender's side `state` includes
`seen`: the receiving session's inbox returned the task and nobody
claimed it yet, so a slow session and a stuck one look different.

```jsonc
// LinkView
{"link": 2, "machine": "gpu-box", "session": "lead", "remote_session": "trainer",
 "direction": "out", "state": "active", "permission_in": "messages",
 "permission_out": "tasks-auto", "proposed": "", "remote_away": false,
 "reason": "", "wrapped": "<remote_message ... kind=\"link\">\npurpose: ...\n</remote_message>"}

// SharedSessionView (never an ID)
{"name": "lead", "purpose": "the paper", "visibility": "private",
 "state": "open", "kind": "live", "agent": "claude"}

// RemoteSessionView and RemoteOfferView (sessions.list)
{"name": "trainer", "kind": "live", "agent": "claude", "state": "open", "wrapped": "..."}
{"label": "trainer", "agent": "claude", "max_permission": "tasks-auto"}

// ListenResult (counts and local names only)
{"unread": 2, "requests": 1, "approvals": 0,
 "pending": [{"link": 2, "machine": "gpu-box", "kind": "message", "count": 2}],
 "closed": false}

// ReviewItemView (review.list)
{"item": "link-3", "kind": "link", "link": 3, "machine": "mac",
 "session": "lead", "permission": "tasks-ask", "wrapped": "..."}

// OfferView (offers.list)
{"machine": "mac", "label": "trainer", "folder": "/srv/train", "agent": "claude",
 "permission": "tasks-auto", "run_mode": "shell", "max_concurrent": 2,
 "idle_timeout_s": 7200, "max_turns_per_run": 40, "run_timeout_s": 1800,
 "runs_per_hour": 30, "runs_per_day": 200, "updated_at": "..."}

// ManagedView (managed.list)
{"name": "trainer-ab12", "machine": "mac", "offer": "trainer", "folder": "/srv/train",
 "run_mode": "shell", "link": 4, "state": "idle", "started": true, "last_active": "..."}

// PeerView
{"alias": "gpu-box", "machine_id": "52 chars", "online": true, "paused": false,
 "paused_by_peer": false, "paired_at": "...", "last_seen": "..."}

// ApprovalView
{"task_id": "01J...", "peer": "gpu-box", "preview": "...", "sha256": "hex",
 "size": 1234, "received": "...", "full": "..."}

// FileView
{"file_id": "01J...", "direction": "in", "peer": "gpu-box", "name": "report.pdf",
 "state": "done", "path": "/home/me/.cravv-connect/files/gpu-box/01J...-report.pdf",
 "reason": "", "size": 3148576}

// StatusResult
{"machine_id": "...", "device_name": "prith-mbp", "relay_url": "https://...",
 "relay_connected": true, "killed": false, "peers": [PeerView],
 "sessions": ["lead (open)"], "outbox_pending": 0, "outbox_held": 0,
 "inbox_unread": 3, "pending_approvals": 1}

// Event (audit.read)
{"ts": "...", "type": "link_request", "peer": "machine id", "alias": "gpu-box",
 "detail": {"direction": "out", "proposed": "tasks-ask"}}
```

- A link's `direction` is `out` when this side asked. `permission_in` is
  what the peer may do on this side, `permission_out` what the peer lets
  this side do there, `proposed` what a pending request asks. `state` is
  `pending`, `active` or `closed` (with `reason`); `remote_away` is set
  while the peer's session is away. The peer's purpose and request note
  are only in `wrapped`.
- A session's `state` is `open`, `away` or `closed`; `kind` is `live` (a
  chat) or `managed`. A managed session's `state` in ManagedView is `idle`,
  `running` or `live` (a human has it open).
- A peer is `online` when the relay connection is up, neither side has
  paused the other, and the peer sent this machine something (including
  delivery receipts) in the last 10 minutes. `last_seen` is when it was
  last heard from since the daemon started.
- `sessions` in StatusResult lists the shared sessions that are open or
  away as `name (state)`, and `inbox_unread` is summed over them.
- File `state` is `held`, `downloading`, `done`, `failed`, `declined`
  (inbound) or `uploading`, `sent`, `failed` (outbound).

## 8. Error kinds

| Kind | Meaning |
|---|---|
| `not_found` | Unknown alias, session, link, task, file, offer, item or pending pairing, or one this connection may not see (they look the same) |
| `not_permitted` | Not allowed on this link (for example a task on a messages link), or a method a managed run may not call |
| `not_shared` | The method needs a session shared on this connection (or the session was closed or taken over by a reattach) |
| `link_closed` | The link is not active |
| `paused` | This machine paused the peer |
| `paused_by_peer` | The peer paused this machine |
| `killed` | The kill switch is on |
| `auth_required` | The method needs `auth.unlock` on this connection, or the chat tier cannot grant this |
| `locked` | Too many wrong passwords; wait 15 minutes |
| `bad_password` | Wrong password |
| `bad_code` | Wrong or expired confirmation code |
| `code_locked` | Too many wrong codes for this item or session; decide with the password |
| `no_desktop` | This machine cannot show a confirmation code |
| `no_decision` | The human did not answer; the item stays pending |
| `rate_limited` | `review.list` more than 6 times a minute |
| `already_claimed` | Another session claimed the task |
| `bad_transition` | The task or file is not in a state that allows this |
| `too_large` | Text over 64 KiB (65536 bytes), or a request or response line over 8 MiB; files over 100 MiB are `path_refused` |
| `path_refused` | The file may not be sent (see `file.send`) |
| `quota` | The peer's inbound file quota is used up |
| `no_session` | The method needs `session.register` first |
| `bad_request` | Invalid params (also an invalid name, purpose, visibility, permission, note, target, folder, offer, or an agent calling an owner's method), or an unknown method |
| `busy` | 32 requests are already running on this connection, a pairing is still in progress, or an offer is at its limit |
| `offline` | No live relay connection, a machine that did not answer discovery within 10 seconds, or the pairing service stopped |
| `pairing_failed` | Wrong code, burned room, or an interrupted or tampered exchange |
| `pairing_expired` | The exchange did not finish within 10 minutes, or it finished more than 10 minutes before `pair.finalize` |
| `auth_unavailable` | No PAM (built without cgo), a `pam_service` that is not on the allowlist, or a PAM stack that accepted a random password |
| `internal` | Anything else (for example low disk space, or a folder that does not exist) |

Clients map kinds back to the Go sentinel errors (`errors.Is` works on the
client side); a kind the client does not know keeps its `kind` and `message`.
````

The table between the markers is what `CRAVV_UPDATE_DOCS=1 go test ./e2e -run TestIPCDocMatchesRegistry -count=1` writes; running it on the file above changes nothing.

- [ ] **Step 3: Run the test**

```sh
go test ./e2e -run TestIPCDocMatchesRegistry -count=1
```

Expected: `ok`.

- [ ] **Step 4: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 5: Commit**

```sh
git add e2e/docs_ipc_test.go protocol/ipc-v1.md
git commit -F - <<'MSG'
protocol: ipc-v1 for v2 (connections, gates, every method with params and results); the method table is generated from the registry and pinned by a test

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 7: protocol: peer-v1 for protocol version 2, updated in place

The frame, the envelope (`"v": 1`) and the sealing label `cravv-connect/peer-v1` did not change in v2, so the document keeps its name and says so at the top; protocol version 2 (`core.ProtocolVersion`, the `min_version` of `control.unsupported`) is what changed inside. The envelope loses its session fields and gains `link_id`; receiving step 7 describes the link gate; the outbox section lists what is sent directly; chat, tasks (with `seen` and the link permissions) and files are on links; new sections 5.5 to 5.7 cover discovery, links (including requests to offers, where `tasks-ask` becomes `messages`) and presence; pairing grants no links. The cryptography, the stale prekey round trip and pairing are unchanged.

**Files:**
- Modify: `protocol/peer-v1.md`

**Interfaces:**

Documents: `core.Envelope`, `core.ProtocolVersion`, `core` kind traits, `internal/core/linkbodies.go`, `daemon.LinkGate`, `daemon.LinkService` (`HandleRequest`, `acceptOffer`, `managedGrant`, `closeLink`, `HandleClosed`), `daemon.PresenceService`, `daemon.Discovery`, `daemon.LinkReplies`, `daemon.TaskService` (`ItemsRead`, `LinkClosed`, `LinkLowered`).

- [ ] **Step 1: Update the document**

Replace the whole of `protocol/peer-v1.md` with:

````markdown
# peer-v1: end-to-end messages between cravv-connect machines

Status: normative. Frame and envelope version 1, protocol version 2.

peer-v1 is what two paired machines say to each other. The name is the
wire format: the frame, the envelope (`"v": 1`) and the sealing label
`cravv-connect/peer-v1` are unchanged since the first release, so this
file is updated in place rather than renamed. Protocol version 2 (session
links) changed what travels inside: chat, tasks and files now carry a
`link_id` and are accepted only on an accepted link between two sessions
(section 5.6); discovery, link and presence kinds were added (sections 5.5
to 5.7); per-machine trust levels were removed. A machine that still sends
link-less (version 1) traffic gets `control.unsupported{min_version: 2}`
(section 5.3). Every message travels
through a relay (see `relay-v1.md`) as an opaque frame: the relay sees the
sender's identity key, the recipient mailbox, a message ID, the frame size and
the time. For files it also sees the recipient, the size and the chunk
count, and for pairing the nameplate. It never sees message contents. This document covers:

1. identities and prekeys,
2. the sealed frame format,
3. how a receiver opens, checks and confirms frames,
4. every message kind and its body, including sessions, links and presence,
5. prekey rotation and the stale prekey round trip,
6. file transfer,
7. the pairing protocol that introduces two machines.

Go reference: `internal/core` (envelope, bodies, kind traits, limits),
`internal/keys`, `internal/sealing`, `internal/filecrypt`, `internal/pake`,
`internal/bindcode`, `internal/pathguard`, and `internal/daemon` (outbound,
inbound, handlers, linkgate, links, links_offer, discovery, presence,
linkreplies, versions, pairing, files, tasks, prekeys, peers, killswitch).

The key words MUST, MUST NOT, SHOULD, and MAY are used as in RFC 2119.

## 1. Conventions

- **JSON.** Field names are `snake_case`. Byte fields are standard base64 with
  padding (RFC 4648 section 4), which is how Go encodes `[]byte`. Receivers
  ignore unknown fields.
- **IDs.** Message, task and file IDs are 26 characters of upper-case Crockford
  base32 (`0-9 A-Z` without `I L O U`): 48 bits of UNIX milliseconds followed
  by 80 random bits, so they sort by creation time. Receivers reject any
  message whose envelope ID, `task_id` or `file_id` (including those in
  `files` lists) is not exactly that, and any `blob_id` that is not 1 to 64
  characters of `[a-z0-9]` (relay-v1 section 6.2). A rejected message is
  dropped and acknowledged, never retried, and gets no `control.delivered`
  when its envelope ID is the bad one. IDs are shown in terminals and agent
  prompts, so nothing else may get through.
- **Timestamps** in envelopes and prekeys are UNIX milliseconds.
- **Machine ID.** `lowercase(base32(SHA-256(ik)))`, RFC 4648 alphabet, no
  padding: 52 characters. It is also the relay mailbox ID. Displays shorten it
  to the first 16 characters.

## 2. Keys

| Key | Algorithm | Lifetime | Use |
|---|---|---|---|
| Identity key (IK) | Ed25519 | Until `cravv-connect reset-identity` | Relay authentication, signs every frame and every prekey |
| Prekey (PK) | X25519 | Current for 7 days; the private key is kept 21 days after it is superseded | Receives sealed frames |

A **signed prekey** is:

```json
{
  "id": "01J8ZQ3W5TAV9M2C4XKQ7N6B1D",
  "pub": "6J3m0p4xJ3g0m3kq3Q1ZyW2VYk0c1C1QfI2yq3VYl2M=",
  "created_at": 1790000000000,
  "sig": "base64 of a 64-byte Ed25519 signature"
}
```

`sig` is the IK signature over the UTF-8 string

```
"cravv-prekey-v1\n" + id + "\n" + base64(pub) + "\n" + decimal(created_at)
```

where `base64` is standard base64 with padding. A receiver MUST verify a prekey
against the peer's IK before storing or using it.

## 3. The sealed frame

### 3.1 Envelope (the plaintext)

```json
{
  "v": 1,
  "id": "01J8ZQ9K7B2V4N6M8P0R2T4W6Y",
  "ts": 1790000123456,
  "from_machine": "q7w...52 chars",
  "to_machine": "k3d...52 chars",
  "link_id": "01J8ZQ3W5TAV9M2C4XKQ7N6B1D",
  "kind": "chat",
  "body": {"text": "the build is green"}
}
```

| Field | Meaning |
|---|---|
| `v` | Always `1` |
| `id` | Message ID, unique per sender; the deduplication key |
| `ts` | Sender clock, UNIX milliseconds |
| `from_machine`, `to_machine` | Machine IDs |
| `link_id` | The link the message travels on. Required on `chat`, `task.create`, `task.update`, `task.cancel` and `file.offer`; omitted on every other kind |
| `kind` | See section 5 |
| `body` | JSON object whose shape depends on `kind` |

The envelope names no session. The receiver takes the sending session and
the local session from its own record of the link (section 5.6), so a peer
cannot address or impersonate a session by writing its name.

### 3.2 Header

```json
{"v":1,"id":"01J8ZQ9K7B2V4N6M8P0R2T4W6Y","from_machine":"q7w...","to_machine":"k3d...","pk_id":"01J8ZQ3W5TAV9M2C4XKQ7N6B1D","suite":"hpke-x25519-sha256-chacha20poly1305"}
```

The **canonical header** is exactly this JSON: the fields in the order `v`,
`id`, `from_machine`, `to_machine`, `pk_id`, `suite`, with no whitespace. Both
the HPKE info and the signature cover these bytes, so a sender MUST produce
them byte for byte and a receiver MUST recompute them from the parsed fields
rather than reuse the received text.

- `id`, `from_machine`, `to_machine` repeat the envelope's values.
- `pk_id` is the ID of the recipient's prekey the frame is sealed to.
- `suite` names the sealing suite. v1 defines one suite (3.3). New suites are
  added by registration; a receiver rejects unknown suites.

### 3.3 Suite `hpke-x25519-sha256-chacha20poly1305`

HPKE (RFC 9180) Base mode with DHKEM(X25519, HKDF-SHA256), HKDF-SHA256 and
ChaCha20-Poly1305, used single-shot:

- **Recipient key:** the X25519 prekey named by `pk_id`.
- **info:** `"cravv-connect/peer-v1\n" + canonical header`. The single-shot API
  has no separate AAD, so the header is bound to the ciphertext through `info`:
  changing any header field makes decryption fail.
- **Plaintext:** the envelope JSON.
- **Payload:** `enc || ciphertext` (32-byte encapsulated key, then the AEAD
  output with its 16-byte tag).

### 3.4 Frame

```json
{"h": {canonical header fields}, "p": "base64(payload)", "s": "base64(signature)"}
```

`s` is the sender's IK signature over

```
"cravv-frame-v1\n" + canonical header + "\n" + payload
```

The marshalled frame MUST be at most 262144 bytes (the relay frame limit).
Anything bigger travels as a file (section 6).

The frame goes to the relay as `send{to: to_machine, id: header.id, frame}`.
The relay-level `id` equals the header ID, so a resend of the same message
reuses it.

## 4. Receiving

A receiver processes deliveries from its mailbox in `seq` order. While the
kill switch is on it handles and acks nothing: the delivery in hand stays at
the relay and inbound processing stops until the switch is turned off. For
each delivery:

1. **Sender.** Look up the paired peer whose IK the relay reports as the
   sender. Unknown senders are dropped.
2. **Paused.** If this machine paused the peer, drop the frame.
3. **Parse** the frame: at most 262144 bytes, `h.v` is 1, `h.id` and `h.pk_id`
   are 1 to 64 ASCII letters or digits, `h.from_machine` and `h.to_machine`
   are 52 characters of `[a-z2-7]`, `h.suite` is 1 to 64 printable ASCII
   characters, and `p` and `s` are non-empty. Otherwise drop.
4. **Open**, in this order:
   1. verify `s` against the peer's IK;
   2. `h.from_machine` equals the signer's machine ID;
   3. `h.to_machine` equals this machine;
   4. `h.suite` is known;
   5. the private prekey `h.pk_id` exists (current or superseded and not yet
      purged). If it does not, reply with `control.stale_prekey` (section 5.3),
      ack the delivery, and stop. The reply is sent at most once per sender and
      message ID (recorded in the deduplication store), so a relay redelivery
      of the same frame does not trigger another resend;
   6. decrypt;
   7. the envelope has `v` 1 and its `id`, `from_machine`, `to_machine` equal the
      header's. This stops a valid signature from being stripped and replaced.
5. **Freshness.** Drop the message if `ts` is more than 21 days in the past or
   more than 10 minutes in the future. Future timestamps are counted, and
   `status` warns about clock skew. An ephemeral kind (`sessions.list`,
   `sessions.listed`, `presence.ping`, `presence.pong`) is then handled at
   once and never deduplicated, recorded or confirmed; its handler drops it
   when it is older than 120 seconds, so frames the relay queued while this
   machine was offline expire harmlessly.
6. **Replay.** If `id` is already in the deduplication store (kept 30 days),
   do not handle it again; for non-control kinds, confirm it again with
   `control.delivered` (the sender may have missed the first receipt).
7. **Handle** it through the handler registered for `kind`. Kinds with no
   handler are dropped. `chat`, `task.*` and `file.offer` pass through the
   link gate first, the single enforcement point for link traffic. It
   admits the message only when:
   1. it carries a `link_id`; otherwise it is dropped and answered with
      `control.unsupported` (at most once per peer per hour);
   2. the link keyed by (sender machine, `link_id`) is `active` here and its
      local session is open or away; otherwise it is dropped and answered
      with `link.closed{unknown_link}` (at most once per link per minute);
   3. the link's `permission_in` allows the kind (section 5.2); a
      `task.create` it does not allow is recorded as rejected and the
      sender is told.

   The handler then works on that link's local session only, and takes the
   peer's session from the link record.
8. **Record** `id` in the deduplication store. This happens only after the
   handler succeeded or failed for good, so a retryable failure leaves the
   message unrecorded and a redelivery runs the handler again. Handlers are
   therefore idempotent on their own: a crash between the handler's write and
   this step must not store the item twice. Tasks and files check their own
   records; a `chat` whose `id` is already in the inbox is not stored again
   (the receiver also keeps a unique index on the chat's `id` and target
   session).
9. **Confirm.** For every non-control kind, queue the ID for a
   `control.delivered` receipt to the sender.
10. **Ack** the relay `seq`.

Dropped frames are logged and counted in `status`. A handler failure that is
worth retrying (a local database error, including a failed deduplication
lookup) is not acked: the receiver stops reading, closes the connection and
reconnects after a backoff (1 second, doubling, at most 5 minutes), and
because acks are cumulative the relay redelivers from that frame. Every other handler failure is logged,
recorded, confirmed and acked, so the sender stops resending an item that
can never be used.

Receipts are batched per peer. The batch is sent when a burst of deliveries
ends and at least every 200 ms.

### 4.1 Sending and the outbox

Every outgoing message goes through the persistent outbox except
`control.paused`, `control.unpaired`, the replies `control.unsupported` and
`link.closed{unknown_link}`, and the ephemeral kinds (`sessions.*`,
`presence.*`): those are sent once, directly, when the relay connection is
up, and never retried. The item stores the envelope, not the frame, and
is sealed again on every attempt to the peer's current prekey. A message whose
sealed frame could not fit the 262144-byte limit is refused before it is
queued (`too_large`).

While the kill switch is on, the daemon still writes items to the outbox (for
example `task.update` notices), but sends nothing until the switch is turned
off; the queued items then go out. Agents cannot add messages while killed,
because the local API refuses sends (ipc-v1).

| Outbox state | Meaning |
|---|---|
| `pending` | Waiting for its next attempt |
| `queued` | The relay answered `queued`; waiting for `control.delivered` (non-control kinds only) |
| `held` | Not sent because a side paused the other (control kinds are never held) |

The relay's answer to `send` decides what happens:

| Relay status | Sender action |
|---|---|
| `queued` | Non-control kinds: mark `queued`; if no `control.delivered` arrives within 7 days (the relay queue TTL), the item goes back to `pending` and is sent again. Control kinds: delete the item (they are never confirmed) |
| `not_allowed` | Within 30 minutes of pairing: the peer has most likely not finalized yet (it has not allowed this machine on the relay); retry with backoff capped at 5 seconds and mark nothing. Later: the peer paused this machine: mark the peer "paused by peer" and hold every non-control item for it until `control.resumed`. Control items keep retrying with backoff |
| `too_large`, `unknown_mailbox` | Drop the item and report it in `status` (items for a peer that is no longer paired are dropped the same way) |
| `queue_full`, `rate_limited`, network error | Back off: 1 second, doubling per attempt, at most 5 minutes |

`control.delivered` deletes the confirmed items (only items addressed to the
peer that sent the receipt). Items older than 21 days are purged. Receivers
never confirm control kinds, so a control item leaves the outbox as soon as
the relay has queued it; the relay's at-least-once delivery covers it from
there.

`control.paused` and `control.unpaired` are sent once, directly, when the
relay connection is up (best effort), because the peer record changes right
after. Sending on a link that is not `active` fails locally at once; nothing
is queued for a closed link.

## 5. Kinds

Size limits: chat text, task instructions, task results and notes are at most
65536 bytes each.

### 5.1 Chat

`chat`: `{"text": "..."}`

```json
{"text": "tests pass on the GPU box, pushing now"}
```

Delivered on any active link, to the link's local session only. While that
session is away the item waits for it; if the link closes first, what it
has not read from that link is dropped.

### 5.2 Tasks

`task.create`:

```json
{
  "task_id": "01J8ZR0A1B2C3D4E5F6G7H8J9K",
  "instructions": "Run the eval suite on checkpoint 12 and report the F1.",
  "files": [{"file_id": "01J8ZR0...", "name": "eval.yaml", "size": 812}]
}
```

`files` lists attachments sent as `file.offer` messages with the same
`task_id` on the same link. The receiver applies its link's `permission_in`:

| `permission_in` on the receiver | Result |
|---|---|
| `messages` | `rejected`, update note `not permitted` |
| `tasks-ask` | `awaiting_approval` until a human on the receiver approves (then `queued`) or denies (`rejected`, note `denied by the receiving human`); expires after 24 hours (`expired`). The session gets an approval notice without the instructions |
| `tasks-auto` | `queued`; unclaimed after 24 hours it becomes `expired` |

A `task.create` whose `task_id` is already known is ignored (a redelivery of
the message that created a queued task only completes a delivery that an
earlier attempt left unfinished).

The receiver reports held and refused tasks with a `task.update`
(`awaiting_approval`, then `queued` after approval, or `rejected`). The
first time the receiving session's inbox returns a queued task, the
receiver sends `task.update` with state `seen` (the task stays `queued`
there), so the sender can tell a session that is slow from one that never
looked. Approval is a human decision on the receiver: in the chat
(`review_pending`, an elicitation form or a confirmation code), or with the
password (`cravv-connect approvals`, the web UI). Approving checks the link
again: it is refused when the link closed or no longer allows tasks.
Lowering a link to `messages` rejects its tasks still waiting (for approval
or a claim); pausing or unpairing the peer closes its links.

`task.update` (receiver to sender):

```json
{"task_id": "01J8ZR0A1B2C3D4E5F6G7H8J9K", "state": "done", "result": "F1 = 0.913", "files": []}
```

| Field | Meaning |
|---|---|
| `state` | `awaiting_approval`, `queued`, `seen`, `claimed`, `running`, `done`, `failed`, `cancelled`, `rejected`, or `expired` |
| `note` | Progress note or reason (`not permitted`, `killed`, `link_closed`, `rate_limited`, ...) |
| `result` | Final result text (with `done`) |
| `files` | Result files, sent as `file.offer` with the same `task_id` |

The sender only accepts updates for its own outbound tasks from the peer
the task was sent to, on the task's link, and never moves a task
backwards: the order is `sent` < `awaiting_approval` < `queued` < `seen` <
`claimed` < `running` < every terminal state. An update ranked lower than the current
state still adds its note, result and files. Updates after a terminal state
are ignored, and an update with state `sent` or an unknown state is rejected.

Receiver-side transitions:

```
awaiting_approval -> queued | rejected | expired | cancelled | failed
queued            -> claimed | expired | cancelled | rejected | failed
claimed           -> running | done | failed | cancelled
running           -> done | failed | cancelled
```

Only the link's local session can claim the task, atomically. The kill
switch fails claimed and running tasks with note `killed`. When a link
closes, every unfinished task on it fails with note `link_closed` on both
sides: the receiver tells the sender when it can, and the sender also fails
its own copy when its side of the link closes, so neither waits for an
update that cannot come. A managed session fails a task it refuses to run
with `rate_limited` or `folder_refused`, and one its run did not finish
with the reason the run ended (`no_result`, `run_timeout`, `run_failed: ...`,
`stopped`, `interrupted`).

`task.cancel` (sender to receiver): `{"task_id": "..."}`. Only the session
that created the task can cancel it, and the sender marks its copy
`cancelled` before sending. The receiver accepts it only from the peer that
sent the task: an `awaiting_approval`, `queued`, `claimed` or `running` task
becomes `cancelled` with note `cancelled by sender`, and the claiming session
(or the link's session, if unclaimed) gets a `task.update` inbox item with
that note. No `task.update` is sent back; unknown or finished tasks are ignored.

### 5.3 Control

| Kind | Body | Sent when |
|---|---|---|
| `control.prekey` | `{"prekey": signed prekey}` | After a rotation, to every peer this machine has not paused |
| `control.stale_prekey` | `{"msg_id": "...", "prekey": signed prekey}` | A frame arrived sealed to an unknown or purged prekey (once per sender and message ID) |
| `control.delivered` | `{"ids": ["...", "..."]}` | Non-control messages were stored (4, step 8) |
| `control.paused` | `{}` | This machine paused the peer (sent directly, then the peer is denied on the relay) |
| `control.resumed` | `{}` | This machine resumed the peer, or finalized pairing with it |
| `control.unpaired` | `{}` | This machine unpaired the peer (sent directly, best effort) |
| `control.relay_moved` | `{"relay_url": "https://..."}` | Reserved for relay changes. Daemons accept and store it (only `https` URLs, or `http` for localhost and private network addresses) but do not send it |
| `control.unsupported` | `{"min_version": 2}` | A peer sent `chat`, `task.*` or `file.offer` without a `link_id` (version 1). Sent directly, at most once per peer per hour; the message itself is dropped |

Handlers:

- `control.prekey`: verify against the peer's IK; store it if its `id` differs
  and `created_at` is not older than the stored one.
- `control.stale_prekey`: store the prekey the same way, then put the outbox
  item `msg_id` back to `pending` so it is sealed again, but only if that item
  is addressed to this peer.
- `control.delivered`: delete the named outbox items addressed to this peer.
- `control.paused`: mark the peer "paused by peer" and hold its outbox.
- Control items are never confirmed; the sender deletes them from its outbox
  as soon as the relay has queued them (section 4.1).
- `control.resumed`: clear the mark and release the held outbox, unless this
  machine has paused the peer itself.
- `control.unpaired`: close every link with the peer, reject its tasks
  awaiting approval, decline its held files, delete the peer, its keys and
  its outbox, deny it on the relay (on the next connection if offline), and
  write an `unpair` audit entry. `control.paused` also closes every link
  with the peer.
- `control.unsupported`: `status` shows that the peer needs this machine to
  upgrade (when `min_version` is higher than this machine's protocol
  version).

### 5.4 File offer

`file.offer`:

```json
{
  "file_id": "01J8ZS2N4P6R8T0V2X4Z6B8D0F",
  "blob_id": "opaque ID assigned by the relay",
  "name": "report.pdf",
  "size": 3148576,
  "chunks": 4,
  "sha256": "base64 of 32 bytes",
  "key": "base64 of 32 bytes",
  "task_id": "01J8ZR0A1B2C3D4E5F6G7H8J9K"
}
```

`task_id` is present when the file belongs to a task. A file travels on a
link like chat (`link_id` in the envelope) and every active link may carry
files. Section 6 describes the transfer.

### 5.5 Sessions and discovery

A session is a named endpoint on one machine (`<alias>/<name>`): a chat
that shared itself, or a managed session the daemon started. Pairing lets
a machine discover the sessions the other one shows it and ask for links;
it grants nothing else.

`sessions.list`: `{"req_id": "<ID>"}`

`sessions.listed`:

```json
{
  "req_id": "01J8ZT0...",
  "sessions": [{"session_id": "01J8ZT1...", "name": "trainer", "purpose": "fine-tunes the wake word model",
                "kind": "live", "agent": "claude", "state": "open"}],
  "offers": [{"offer_id": "01J8ZT2...", "label": "trainer", "agent": "claude", "max_permission": "tasks-auto"}]
}
```

- Both are ephemeral: sent directly, never confirmed or retried, dropped
  when older than 120 seconds. The asker matches the answer by `req_id` and
  waits at most 10 seconds.
- The receiver answers at most 30 lists per peer per minute and lists only
  its open and away sessions whose visibility includes the asker
  (`private`, `all-peers`, or named peers), plus the managed-session offers
  its owner made to the asker (labels only: never the folder or the
  limits). The project folder is never sent.
- `name` is 1 to 32 characters of `[a-z0-9-]`, not starting with `-`;
  `purpose` one line of at most 120 characters; `kind` `live` or `managed`;
  `state` `open` or `away`. The asker drops entries that break these rules
  and shows the purpose only inside a `<remote_message>` wrapper.

### 5.6 Links

A link connects one session on each machine. The requester mints its
`link_id` (an ID as in section 1); each side stores the link keyed by
(remote machine, `link_id`) with its own local number, and derives the
peer's session only from that record. Each side sets `permission_in`, what
the other side may do to it: `messages` (chat and files), `tasks-ask`
(also tasks that each need a human decision on this side) or `tasks-auto`
(also tasks its agent may carry out without asking).

| Kind | Body |
|---|---|
| `link.request` | `{"link_id", "from_session": {"id", "name", "purpose"}, "to_session_id" or "offer_id", "proposed_permission", "note"}` |
| `link.accepted` | `{"link_id", "to_session": {"id", "name", "purpose"}, "granted_permission"}` |
| `link.rejected` | `{"link_id", "reason"}`: `declined`, `not_found`, `busy`, `policy` or `timeout` |
| `link.closed` | `{"link_id", "reason"}`: `closed_by_peer`, `session_closed`, `paused`, `unpaired`, `killed`, `presence_timeout` or `unknown_link` |
| `link.state` | `{"link_id", "state": "active" or "away", "permission_in"}` |

All five travel through the outbox and are confirmed like chat, except the
`link.closed{unknown_link}` reply (sent directly).

**Request.** `proposed_permission` is what the requester would like to do
on the other side; `note` is at most 280 characters. The requester's own
side of the new link lets the peer send messages only. The receiver:

1. ignores a `link_id` it already has (a redelivery);
2. rejects `busy` past 10 new requests from the peer in a minute or while 5
   requests from it are pending, `policy` for a malformed request, and
   `timeout` for one sent more than 10 minutes ago;
3. rejects `not_found` for a session that is missing, closed or not visible
   to the requester, and for an unknown offer. These answers are identical,
   so a peer cannot probe for private sessions, and nothing is stored;
4. otherwise stores the request as `pending`, tells the target session (an
   inbox notice and the listener) and shows a desktop notification.

A pending request is decided once, on the receiving side, by a human:
accepted (at the level asked or lower) or rejected (`declined`).
Accepting at `messages` or `tasks-ask` takes a chat decision or the
password; accepting at `tasks-auto` takes the password. A request pending
for 10 minutes is rejected with `timeout`.

**Request to an offer.** With `offer_id` instead of `to_session_id`, the
receiver checks the offer's rules, caps and concurrency limit, creates a
managed session named after the offer's label plus a 4-character suffix,
and answers `link.accepted` at once with `granted_permission` the lower of
the proposed level and the offer's permission, except that `tasks-ask`
becomes `messages` (a managed session has nobody to ask). The owner's
password-gated offer was the approval. It rejects `not_found` for an offer that does not
exist or was made to another machine, `busy` at a limit, and `policy` when
the offer's folder no longer passes its checks.

**Accepted.** The requester activates its pending link and records the
acceptor's session (`to_session`) and `granted_permission` (what it may do
there). An answer for a link that is not pending there gets
`link.closed{unknown_link}`.

**State.** Each side sends `link.state` when its session goes away or
comes back and when it changes `permission_in`. It is informational: each
side enforces only its own `permission_in`. Lowering needs nothing;
raising, or accepting at `tasks-auto`, needs the password.

**Close.** A link closes when either session closes, either side
disconnects it, on pause or unpair of the machine, on the kill switch, or
on presence timeout, and a closed link never reopens. The side that closes
sends `link.closed` (except on pause and unpair, where `control.paused` and
`control.unpaired` already say so) and tells its session. The receiver of
`link.closed` closes its side and tells its session; it never answers a
`link.closed`, so two sides that both lost a link cannot bounce replies.
Closing fails the link's unfinished tasks (section 5.2), stops its file
downloads and declines its held files, drops what an away session had not
read from the link, and closes a managed session whose link it was.

### 5.7 Presence

| Kind | Body |
|---|---|
| `presence.ping` | `{"ts": 1790000123456, "link_ids": ["..."]}` |
| `presence.pong` | `{"ts": 1790000123456, "link_ids_open": ["..."]}` |

- While a peer has at least one active link, each side pings it every 30
  seconds, naming those links (at most 1000).
- The receiver answers with the named links that are open on its side
  (`pending` counts as open, because its `link.accepted` may not have
  arrived yet), echoing `ts`. A ping is also fresh evidence for the links
  it names.
- A link is dead after 150 seconds without fresh evidence, or as soon as a
  fresh pong to a recent ping leaves it out. It closes with
  `presence_timeout` and `link.closed` is queued, so the peer converges
  when it is reachable again.
- Both kinds are ephemeral (section 4, step 5): a ping or pong older than
  120 seconds, or more than 10 minutes in the future, is ignored.

A session that closes sends `link.closed{session_closed}` on each of its
links at once, through the outbox, so the peer learns within seconds while
both machines are online and within 150 seconds when a machine drops.

## 6. Files

**Sender**

1. The path passes the outbound rules: a regular file inside the session's
   project folder or a folder added with `cravv-connect allow-path`, no
   component starting with `.`, not a secret-looking name (case-insensitive:
   `.env*`, `id_*`, `credentials*.json`, `service-account*.json`, or ending in
   `.pem`, `.key`, `.env`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.kdbx`, `.ppk`,
   `.ovpn`), symlinks resolved and checked again, the file opened without
   following symlinks and confirmed to be the file that was checked, exactly
   one hard link, at most 104857600 bytes. A peer this machine paused is
   refused.
2. Generate a random 32-byte key and a new `file_id`.
3. `POST /v1/blobs` with the recipient's IK, the size and
   `chunks = ceil(size / 1048576)` (1 for an empty file).
4. Encrypt chunk `i` with XChaCha20-Poly1305:
   - nonce: `SHA-256(file_id)[0:20] || uint32_be(i)` (24 bytes),
   - AAD: `file_id || uint32_be(i) || byte(last)` where `last` is 1 for the
     final chunk,
   - so a chunk moved to another index, another file, or a truncated file
     fails to decrypt.
5. Upload every chunk, hash the plaintext with SHA-256, write an audit entry,
   and send `file.offer` through the outbox.

**Receiver**

A `file.offer` on an active link is downloaded at once, for the link's
session. (Files held for a human, accepted with `cravv-connect files
accept <file_id>`, exist only from before the upgrade to protocol version
2; pausing or unpairing the peer, or closing the link, declines them.)

The offer is checked first: `file_id`, `task_id` (when present) and the
message ID are IDs as in section 1, `blob_id` is 1 to 64 characters of
`[a-z0-9]`, the size is at most 104857600 bytes, `chunks` matches the
size, and `sha256` and `key` are 32 bytes each; otherwise it is dropped.
Before downloading, the receiver checks the per-peer quota (1 GiB by default,
counting files downloading now plus files downloaded within the last 30
days) and keeps 64 MiB of disk free on top of the file. A declined or failed offer creates a local inbox notice and sends
the sender a chat: `cravv-connect: file "<name>" (file_id <id>) was not
received: <reason>`.

The download writes `<target>.part`, one chunk at a time, recording the next
chunk index so a restart resumes where it stopped. After the last chunk it
checks the size and SHA-256 (on a mismatch it deletes the part file and the
next attempt starts again from chunk 0), then hard-links the part file to the target (it
never overwrites an existing file) and deletes the blob. A failed attempt is
retried after 2 seconds; after 3 attempts the offer is marked failed.
Turning the kill switch on stops running downloads; they continue from the
recorded chunk when the switch is turned off.

The target is `files/<alias>/<message id>-<name>` in the state directory,
where `<name>` is the peer's name reduced to its base name, with every
character outside `[A-Za-z0-9._-]` replaced by `_`, leading dots removed, at
most 100 characters, and `file` when nothing remains. The final path is
checked to be inside `files/`.

## 7. Prekey rotation and the stale prekey round trip

- The daemon creates its first prekey on start.
- Every minute a maintenance pass rotates the prekey once it is 7 days old
  (not while the kill switch is on): it stores a new prekey, marks every other
  one superseded, and sends `control.prekey` to each peer it has not paused.
- The same pass deletes private prekeys superseded more than 21 days ago. That
  covers the relay's 7-day queue plus 14 days of a sender retrying.
- A peer that missed the rotation (it was paused, or it was offline long
  enough for the old key to be purged) seals to a `pk_id` the receiver no
  longer has. The round trip:

```
A (sender)                         relay                      B (receiver)
seal(msg m1, pk_id=old) ---------> queue --------------------> Open: unknown pk_id
                                                               ack the delivery
                                   queue <-------------------- control.stale_prekey{msg_id: m1, prekey: current}
store B's current prekey  <------- deliver
outbox m1 -> pending
seal(m1, pk_id=current) ---------> queue --------------------> Open ok, dedup, handle
                                   queue <-------------------- control.delivered{ids:[m1]}
delete m1 from outbox     <------- deliver
```

The first copy never reaches the deduplication store under its message ID (it
could not be opened; only the once-per-message stale reply marker is stored),
so the re-sealed copy is handled exactly once. The e2e test
`TestStalePrekeyResend` exercises this path.

## 8. Pairing (pair-v1)

Pairing introduces two machines over a relay pairing room (`relay-v1.md`
section 5). The creator is side **A**, the joiner side **B**.

### 8.1 Bind code

`CRAVV-NNNN-SSSS-SSSS` in Crockford base32 (`0-9 A-Z` without `I L O U`):

- `NNNN` is the relay-assigned nameplate (the room ID the relay sees).
- `SSSS-SSSS` is the secret: 5 random bytes (40 bits). It never leaves the two
  machines.
- Input is case-insensitive; spaces and hyphens are ignored; `O` reads as `0`,
  `I` and `L` read as `1`; `U` is refused.

### 8.2 Setup

A (after `auth.unlock`):

1. requires a live, registered mailbox;
2. `invite_request` on its mailbox (single-use, 10 minutes);
3. `room_create`, which returns the nameplate and a creator token;
4. builds the code from the nameplate and a fresh secret;
5. opens `/v1/pair/{nameplate}?token=<creator_token>` and waits (at most 10
   minutes) for the joiner.

B (after `auth.unlock`) parses the code and opens `/v1/pair/{nameplate}`
without a token. B needs no mailbox yet.

### 8.3 Exchange

Both sides run the same steps. Each step sends one room message
(`{"t":"msg","data":"<base64>"}`) and then receives the peer's. Any failure
ends the exchange with "pairing failed", and closing the room burns it.

1. **SPAKE2.** Password: the normalized code string `CRAVV-NNNN-SSSS-SSSS`.
   Group: Ed25519 (the magic-wormhole and python-spake2 variant, `gospake2`).
   Identities: `cravv-connect/pair-v1/A` and `cravv-connect/pair-v1/B`. Each
   message is 33 bytes: the side byte `A` or `B` and a 32-byte group element.
   Both derive the 32-byte key `K`.
2. **Key confirmation.** Each side sends
   `HMAC-SHA256(key = HKDF-SHA256(ikm = K, salt = none, info = "cravv-connect/pair-v1/confirm", 32 bytes), msg = "A" or "B")`
   for its own side and checks the peer's tag in constant time. A wrong code
   fails here.
3. **Payload.** Each side sends its payload sealed with ChaCha20-Poly1305:
   - key: `HKDF-SHA256(ikm = K, salt = none, info = "cravv-connect/pair-v1/aead", 32 bytes)`,
   - nonce: 12 bytes, all zero except the last byte: `0` for A, `1` for B,
   - AAD: `cravv-connect/pair-v1/payload`,
   - plaintext:

   ```json
   {
     "ik": "base64 of the 32-byte Ed25519 identity key",
     "prekey": {"id": "...", "pub": "...", "created_at": 1790000000000, "sig": "..."},
     "relay_url": "https://relay.example.com",
     "name": "prith-mbp",
     "invite": "only from A: the single-use relay invite"
   }
   ```

4. **Validation.** The received IK is 32 bytes, is not this machine's own IK,
   the prekey signature verifies against it, and `relay_url` is an `https` URL
   (plain `http` only for localhost, private network addresses (RFC 1918, unique local IPv6, 100.64.0.0/10) and single-label `.local` names).
5. **Ack.** Each side sends the sealed constant `cravv-connect/pair-v1/ok`
   (same key and AAD, nonce last byte `2` for A, `3` for B) and checks the
   peer's. A payload tampered with in either direction therefore fails on both
   sides.

### 8.4 Finalize

Each side shows the human the peer's short machine ID and a suggested alias:
the peer's `name` lowercased, every run of characters outside `[a-z0-9]`
turned into one `-`, trimmed, at most 24 characters, or `peer-<6 characters of
the machine ID>` when nothing remains. The peer-chosen name is never shown
raw. The human picks the alias (1 to 24 characters of `a-z 0-9 -`, starting
with a letter or digit). `pair.finalize` then:

1. on a machine without a mailbox (the joiner), registers one with the invite
   from A's payload and waits for the connection;
2. stores the peer: IK, machine ID, alias, prekey, relay URL. The peer gets
   no links: it can discover sessions and ask for links, and each link is
   decided on its own (section 5.6);
3. adds the peer's IK to the relay allow-list;
4. queues `control.resumed` to the peer: the side that finalized first may
   already have sent and been told `not_allowed`, and this clears any pause it
   recorded and releases what it held;
5. writes an audit entry (`pair`, with the role `creator` or `joiner`).

A pending pairing lives 10 minutes; a successful exchange gets a fresh 10
minutes for the human to finalize. There is no fingerprint comparison step:
`cravv-connect peers` shows every machine ID for a later check.
````

- [ ] **Step 2: Check it**

```sh
grep -n 'trust\|chat-only\|autonomous' protocol/peer-v1.md
grep -c $'\u2014' protocol/peer-v1.md
```

Expected: one line, `12:to 5.7); per-machine trust levels were removed. A machine that still sends`, and `0`.

- [ ] **Step 3: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 4: Commit**

```sh
git add protocol/peer-v1.md
git commit -F - <<'MSG'
protocol: peer-v1 for protocol version 2 (link_id, the link gate, sessions, links and presence kinds, control.unsupported; trust levels removed), updated in place

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 8: docs: security for v2

The threat model is rewritten around links. It opens with the model (machines, sessions, links, the gate) and the tier table of spec 10, then updates what is defended (a hostile machine now reaches only its links; private sessions look missing), and states the honest limits: other processes of the same user (the no-password actions, `store.db`, a chat's connection or tokens, bounded at the chat tier), the macOS notification database with Full Disk Access, the web UI cookie across ports, managed `shell` mode acting as your user (reading `~/.cravv-connect/store.db`), relay metadata including presence timing, and the LAN test relay. New sections: tokens and session identity (reattach, wake, run), and managed sessions, merging the containment section the Phase 3 review added on `main` (flags per run mode, `--restricted`, the allowlisted environment, process groups, the peer-PID check, caps, opening, transcripts). The password table, the kill switch's while-killed list (taken from the generated IPC table), storage and the audit event list follow the code.

**Files:**
- Modify: `docs/security.md`

**Interfaces:**

Documents: `daemon.ClaudeAdapter` (`runContainment`, `runModeArgs`, `runEnv`), `childenv`, `daemon.SessionHost` (`BindRun`, `InRun`), `mcpserver` wake files, `daemon.ConfirmCodes`, the web UI launcher limits, the audit event types, and the gates of `protocol/ipc-v1.md` section 5.

- [ ] **Step 1: Rewrite the document**

Replace the whole of `docs/security.md` with:

```markdown
# cravv-connect security

This page says what cravv-connect protects against, what it does not, and the
exact limits behind each claim. Protocol details are in
`protocol/peer-v1.md`, `protocol/relay-v1.md` and `protocol/ipc-v1.md`.

## The model

- **Machines** pair once, with a one-time code and your login password on
  both sides. Pairing only lets the two machines see the sessions each
  shows the other and ask for links. It gives nothing to an agent.
- **Sessions** are named endpoints on one machine: a chat that shared
  itself (`session_share`), or a managed session the daemon started for a
  paired machine from an offer you set. A session's identity is bound to
  the connection that shared it, never to an argument (see "Tokens and
  session identity").
- **Links** connect one session on each machine and are the only path for
  messages, tasks and files. Each side decides what the other may do to it
  (`permission_in`): `messages` (chat and files), `tasks-ask` (also tasks,
  each approved by a human on the receiving side) or `tasks-auto` (also
  tasks the agent may run without asking).
- **The receiving daemon enforces the link**, in one gate in front of the
  chat, task and file handlers. A message is taken only when the link
  exists here and is active, the sender is the link's other machine, the
  local session is the link's own end, and `permission_in` allows the
  kind. The agent is never trusted to check. Inbox, tasks and files are
  kept per session and per link.
- **Decisions are tiered.** Opening something up needs a human; the wider
  the opening, the stronger the check. Cutting off needs nothing:

| Decision | Who can make it |
|---|---|
| Accept a link at `messages` or `tasks-ask`; approve one task on a `tasks-ask` link | The human, in the chat (an elicitation form, or a 4-digit code from a desktop notification), or with the password in the CLI or web UI |
| Accept or raise a link to `tasks-auto`; raise any link; edit managed-session offers; pair and join; resume after the kill switch; allow another folder for files; reset the identity | Only with the password, in the CLI or web UI |
| Reject a request or deny a task; restrict or disconnect a link; pause, resume-peer or unpair a machine; close a managed session; the kill switch; stop the daemon | Anyone local, agents included: no password, so cutting off stays easy |

## What we defend against

- **A malicious or compromised relay.** Messages and file chunks are
  encrypted end to end (HPKE to the recipient's prekey for messages,
  XChaCha20-Poly1305 with a per-file key for files) and every frame is signed
  with the sender's Ed25519 identity key. The relay can drop, delay, reorder or
  replay frames. Replays and duplicates are removed by message ID (30 days),
  messages older than 21 days or more than 10 minutes in the future are
  rejected, and tampered or re-signed frames fail verification. Frames from
  unknown or paused peers are dropped. A frame sealed to an unknown prekey
  gets at most one `control.stale_prekey` reply per peer and message ID, and
  only after its signature verifies. Presence and discovery frames older
  than 120 seconds are ignored, so the relay cannot replay an old "the link
  is open". The relay never sees message contents, file contents, file
  names, aliases, session names, purposes, link notes or offer labels.
- **Network attackers.** Everything above, plus TLS to the relay in
  production (`https`/`wss`). `cravv-connect init` accepts an `http://` relay
  URL for local testing; relay addresses learned from another machine (a
  pairing payload, a join code, `control.relay_moved`) must be `https`
  unless they are this machine or a private network (loopback, RFC 1918,
  unique local and Tailscale addresses, single-label `.local` names).
  Mailbox logins and blob requests are signed over the relay's normalized
  origin, so a signature made for one relay cannot be replayed against
  another, and blob request signatures expire after 5 minutes.
- **Strangers.** A relay mailbox only accepts frames from identity keys its
  owner allowed, which happens only when pairing (and again when you resume a
  peer you paused). Creating a mailbox needs the relay admin token (first
  machine) or a single-use invite that a member requests and sends inside the
  encrypted pairing exchange. Joining a pairing needs the full bind code: the
  relay sees only the 4-character nameplate, the 40-bit secret is the SPAKE2
  password, a room allows exactly one joiner and lives 10 minutes, and a wrong
  code fails key confirmation and burns the room. An attacker gets one online
  guess per code. `setup --join` shows the relay in a join code and asks
  before using it. Relays also cap abuse: at most 20 unused invites per
  member, 2 GiB of live blobs per member and 50 GiB per relay, a 10000-frame
  or 50 MB queue per mailbox, and per-mailbox and per-IP rate limits (the Go
  reference relay also allows at most 8 open pairing rooms per member;
  relay-cf allows at most 256 live blobs per member).
- **A paired machine that turns hostile.** It can only do what your
  sessions' links let it:
  - It sees only the sessions you made visible to it. A session it cannot
    see, a closed one and a missing one give the same `not_found`, and a
    link request to one stores nothing and notifies nobody, so it cannot
    probe for private sessions.
  - Its link requests wait for your decision. It may have at most 5
    pending and send at most 10 a minute; a request expires after 10
    minutes. It may list your sessions at most 30 times a minute.
  - On a `messages` link its tasks are rejected; on a `tasks-ask` link each
    task waits for a human here (expiring after 24 hours), and agents
    cannot read a held task's text: the chat gets a notice without it, and
    `get_task` does not return it. Approving checks the link again.
  - It cannot raise what it may do: each side's `permission_in` is set only
    on that side, and raising needs your password.
  - It cannot name the session it speaks for or the one it reaches: the
    receiver takes both from its own link record, never from the message.
    It cannot reach a session of yours it has no link with, and traffic on
    one link is never shown to another session.
  - It cannot start agents here unless you made it an offer (with your
    password), and then only in that offer's folder, run mode and limits.
  - It cannot choose where files are saved: every incoming file goes to
    `files/<alias>/<message id>-<sanitized name>` inside the state directory,
    is never written over an existing file, and never becomes a dotfile.
  - It cannot update tasks it did not receive, cancel tasks it did not
    send, or act on a link that is closed or not its own.
  - It cannot choose IDs that fake local output: every message, task, file
    and link ID it sends must be exactly 26 upper-case Crockford base32
    characters (what `core.NewID` makes), and every blob ID 1 to 64
    characters of `[a-z0-9]`. A message with any other ID is dropped on
    receipt and not retried, so control characters, escape sequences or
    look-alike lines never reach approval screens, logs or agent prompts.
    Session names it sends must be `[a-z0-9-]`; its free text (purposes,
    notes) reaches agents and pages only inside the wrapper below.
- **Prompt injection that tries to make a local agent open things up.**
  Accepting a link, approving a task, granting `tasks-auto`, editing
  offers and pairing all need a human (a form or code the model never
  sees, or the password), which agents do not have.

## What we do not defend against

These are the honest limits. Read them before you rely on it.

- **Prompt injection through free text.** A message can still try to talk an
  agent into doing something within that agent's own permissions. Mitigations:
  - items returned by `check_inbox` and `wait_for_message` are wrapped in
    `<remote_message ...>`. The body is XML-escaped, and invisible characters
    (Unicode tag characters, bidi overrides and isolates, zero-width
    characters, the BOM) are removed. Attribute values are cleaned and capped
    at 64 characters. The MCP server's standing instructions tell the agent
    that this content is not from the user and that chat is information, not
    a command;
  - the task tools return text the peer wrote (an inbound task's
    instructions and file names; an outbound task's result, the peer's
    progress notes and result file names) only in the task's `wrapped`
    field, with the same wrapper and escaping. The raw fields are left
    empty, so no tool hands an agent unwrapped peer text;
  - the listener line, the hooks and desktop notifications never print
    message bodies or names chosen by a peer, only local aliases, link
    numbers and counts (a confirmation code's notification shows a held
    task's text last, after the daemon's own words, cut at 280 characters);
  - the CLI removes every control character (including newlines, carriage
    returns, tabs and escape sequences), bidi and zero-width characters from
    peer-derived strings it prints, and prints multi-line task text in
    `cravv-connect approvals` with each line prefixed by `| `, so it cannot
    pass for the CLI's own lines; the web UI escapes the same text;
  - outgoing files are restricted (below);
  - your agent's own permission settings remain the last line of defense.
    Keep them as strict as you would for untrusted input. A link at
    `tasks-auto` means the other session's tasks run with your agent's
    permissions without a human in between.
- **Other processes running as the same OS user.** They can do everything
  you can do without the password:
  - talk to the daemon socket, run the CLI, or start the web UI
    (`cravv-connect ui`) and use every no-password action (reject, restrict,
    disconnect, pause, unpair, kill);
  - read and change `store.db` (on Linux it also holds the identity seed),
    edit the audit log, and read files you received;
  - share a session of their own, which peers could then ask to link with;
  - read a chat's wake file (counts only) and, where the system lets a
    process read another's memory or environment, take tokens from the
    MCP server or a managed run.

  With a chat's own IPC connection (or its reattach token) such a process
  could answer that chat's decisions at the chat tier. The damage is
  bounded: it can accept a link at `messages` or `tasks-ask` or approve a
  single task, never grant `tasks-auto`, raise a link or edit offers.
  Wrong passwords it tries count toward your 15-minute lockout.
- **The macOS notification database.** macOS keeps delivered notifications,
  confirmation codes included, in a database under your Library folder. A
  process with Full Disk Access can read it (see "Chat decisions and
  confirmation codes").
- **The web UI's cookie across ports.** Browsers send a cookie for
  `127.0.0.1` to every port on that address, so a local program the browser
  visits on another port can read the UI's session cookie. With it, it can
  do what needs no password until the session ends or the cookie rotates;
  never anything that needs the password (see "The web UI").
- **A managed session in run mode `shell`.** Its agent runs commands as
  your user, on behalf of the other machine, and Bash is not confined to
  the folder. It can read anything you can, `~/.cravv-connect/store.db`
  and your SSH keys included, and edit your `~/.claude` settings. The
  `Bash(cravv-connect:*)` rule and the daemon's check of a run's processes
  are speed bumps: a process that leaves the run's process group can talk
  to the daemon like any process of yours. Offer `shell` only to a machine
  you trust as much as yourself (the CLI and the web UI make you type
  `shell` to confirm). A conversation a peer drove opens with your normal
  settings when you run `session open`; review it before continuing.
- **Anyone who knows the login password**, or a setup with passwordless sudo
  that an agent can use to read the password or change the daemon.
- **Traffic metadata visible to the relay** (section "What the relay sees").
- **A relay that drops control messages.** Control messages (such as a pause
  or prekey notice) leave the sender's outbox once the relay has queued them
  and are not resent, so a relay that drops one is not detected. Presence
  still closes links that stopped answering within 150 seconds.
- **Secrets pasted into messages.** Free text cannot be policed. An agent can
  still put a secret into a chat message or a task result.
- **The LAN test relay.** The relay `cravv-connect setup` can start for a
  trial listens on every interface over plain http, keeps everything in
  memory, and stops when the machine restarts. Anyone on the network can
  reach it; the admin token setup made for it is in its environment (on
  Linux any process of the same user can read that). It still only ever
  sees ciphertext. Use it only on a trusted network, and deploy the
  Cloudflare relay for real use.

## Tokens and session identity

A session is bound to the IPC connection that shared it. No tool or IPC
method takes a session ID, no IPC view returns one, and none appears on a
command line, so a local process cannot act as a session by naming it.
Three secrets let the right client, and only it, reach a session:

| Token | Given to | Allows | Where it lives |
|---|---|---|---|
| Reattach token | The MCP server that shared the session | Taking the session back after an MCP reconnect or a daemon restart, from the same agent and project folder only | The MCP server's memory. Never on a command line, never to the model, never on disk outside the daemon's store (which keeps only its SHA-256) |
| Wake token | The chat's listener | Counts of what is pending (local aliases and link numbers only), nothing else | A `0600` file `~/.cravv-connect/wake/wake-<32 hex>` (folder `0700`, written with `O_EXCL`), removed when the session closes or the MCP server exits. The listener refuses a file readable by others or owned by another user. The command the agent runs names the file, never the token |
| Run token | One managed run's `cravv-connect mcp` | Acting as that managed session only, with the run methods only (its messages, tasks, files and link), for that run and the first connection that binds it | A `0600` MCP config file `~/.cravv-connect/runs/run-<id>.json`, removed as soon as the run's MCP server binds (stale ones are removed when the daemon starts). Never in the agent's own environment or on a command line. The daemon keeps its hash in memory and revokes it when the run ends |

All three are 32 random bytes from `crypto/rand`. A reattach takes the
session away from the old connection at once. When a chat's connection
drops, the session is away for up to 10 minutes: its links stay open and
what arrives waits; then it closes and its links close.

Hooks find the chat's session through Claude Code's chat ID: Claude Code
starts the MCP server with it in `CLAUDE_CODE_SESSION_ID` and gives it to
every hook as `session_id`, so two chats in one folder are not confused. After `/clear` the
chat ID changes while the MCP server keeps the old one: the hooks then stay
silent for that chat (the listener keeps working).

## The password gate

**Only type your login password into the `cravv-connect` CLI in your own
terminal, or into the local web UI that `cravv-connect ui` opened.** No
agent, chat or message will ever legitimately ask you for it.

How it works:

- The CLI reads the password from `/dev/tty` with echo off, never from stdin
  or an argument, so a process without a terminal (such as an agent running a
  shell command) cannot be prompted.
- The CLI sends it over the local socket (mode `0600`, directory `0700`; the
  daemon also refuses any connection whose peer credentials show another
  UID) in `auth.unlock`. The daemon checks it with PAM for the user the
  daemon runs as. Only services that check the login password are allowed:
  `chkpasswd` (default) or `checkpw` on macOS, `login` (default) or
  `system-auth` on Linux, set with `pam_service` in `config.toml`; any other
  value is refused. After the password, PAM's account check must also pass
  (expired or disabled accounts are refused).
- The first time the daemon starts with a PAM service, and again whenever
  that service's file in `/etc/pam.d` changes (size or modification time), it
  checks that a random password is rejected as a wrong password and refuses
  to start if it is accepted. This counts as one failed login for that
  account. If PAM fails in any other way, the daemon starts but every unlock
  fails, and `status` reports "password check is not working"; that result is
  not remembered, so the test runs again at the next start. The PAM
  transaction names a pseudo tty (`PAM_TTY=cravv-connect`).
- PAM needs cgo; a binary built with `CGO_ENABLED=0` refuses every
  password-gated action (the CLI reports "password check unavailable").
- A successful check unlocks only that one connection, for 10 minutes. It is
  not stored anywhere. The web UI never keeps an unlock (below).
- The daemon checks the password again inside each human-only action, so a
  missing check in one layer does not open the gate.
- After 5 wrong passwords in a row, every attempt fails for 15 minutes without
  reaching PAM. The count and lockout are stored in `store.db`, so restarting
  the daemon does not reset them; if that state cannot be read, the daemon
  starts locked.
- Every non-empty attempt, including attempts refused during a lockout, is
  written to the audit log with the user name and the outcome, never the
  password. The daemon does not keep the password after the check, but Go
  cannot reliably erase a string from memory, so it stays in freed memory
  until it is overwritten.

Needs the password:

| Action | Command |
|---|---|
| Pair and join | `cravv-connect setup`, `setup --join`, `pair`, `join <code>` |
| Accept a link request (at any level) outside the chat | `cravv-connect link accept <link>` |
| Raise what a link allows (including to `tasks-auto`) | `cravv-connect link permit <link> <level>` |
| Ask for a link on a session's behalf from the web UI | Sessions page, Connect |
| Create, change or remove a managed-session offer | `cravv-connect offers set`, `offers remove` |
| List, approve or deny held tasks outside the chat | `cravv-connect approvals`, `approve <id>`, `deny <id>` |
| Accept a file held before the upgrade to v2 | `cravv-connect files accept <id>` |
| Turn the kill switch off | `cravv-connect resume` |
| Allow another folder for outgoing files | `cravv-connect allow-path <dir>` |
| New identity | `cravv-connect reset-identity` |

Does not need it: rejecting a link request (`link reject`), restricting or
disconnecting a link, `pause`, `resume-peer`, `unpair`, closing a managed
session (`session close`), `kill`, stopping the daemon, and everything
agents do on their own links (sending, reading, working on tasks they
received, cancelling tasks they sent). `resume-peer` is the one widening
without a password, because it only restores what you chose when pairing;
it reopens no link.

### Chat decisions and confirmation codes

`review_pending` lets the human behind a chat decide link requests and held
tasks on `tasks-ask` links without the password, at the chat tier: it can
never grant `tasks-auto` or raise a permission. The answer comes from an MCP
form the human fills in, or, when the agent cannot show one, from a 4-digit
confirmation code:

- Only a real answer counts: the form's action `accept` with one of the
  choices it offered. A dismissed or auto-declined form (the VS Code
  extension declines forms without showing them) leaves the item pending.
- The code is shown only in a macOS desktop notification. The daemon hands
  the notification script to `osascript` on stdin, never as an argument, so
  the code does not appear in `ps`. It never travels over the daemon socket
  or reaches the model; the human reads it and types `accept <code>` in the
  chat.
- The notification starts with the code, then says in the daemon's own words
  what is being decided. Text the peer wrote (a task's instructions) comes
  last, after `From <alias>/<session>:`, cut at 280 characters, so a peer
  cannot put a fake code before the real one.
- A code is valid for 10 minutes and is used up once the decision is
  applied (a decision that fails leaves it valid for a retry).
- An item takes at most 3 wrong codes over its whole life; asking for a new
  code does not reset the count. After the third, no code ever works for that
  item again: the human decides it with the password, in a terminal
  (`cravv-connect approvals`, `cravv-connect links`) or in the web UI
  (`cravv-connect ui`).
- A chat session takes at most 10 wrong codes in 24 hours over all its items.
- These counts are stored in the daemon's database, so restarting the daemon
  does not reset them.
- On Linux there is no desktop notifier (`notify-send` only takes the text
  as arguments), so there is no code path: the password path is the only way.

Residual risk: macOS keeps delivered notifications in its notification
database under your Library folder. A process with Full Disk Access can read
it. If the terminal or IDE that runs your agent has Full Disk Access, a
shell command the agent runs inherits it and could read the code and approve
on its own, within the chat tier (never `tasks-auto`). Do not give Full Disk
Access to the terminal or IDE your agents run in, or decide with the password
instead.

## The web UI

`cravv-connect ui` asks the daemon to serve a local page on `127.0.0.1`
(random port) and opens it with a one-time launch link. Each browser session
talks to the daemon over its own in-process IPC connection, so the gates,
tiers and password lockout above apply unchanged. Agent connections cannot
start it.

- **Every request** must name the UI's own host (`127.0.0.1:<port>` or
  `localhost:<port>`, which blocks DNS rebinding) and gets
  `Cache-Control: no-store` and a strict Content Security Policy. **Every
  form post** also needs the session cookie (`HttpOnly`, `SameSite=Strict`),
  the session's form token and the UI's own `Origin`.
- **The launch link** carries a token that works once and for 2 minutes; it
  is swapped for the cookie and removed from the address bar.
- **No unlock outlives a request.** The browser session's own connection is
  never unlocked. Every action that needs the password carries it in its
  own form: the UI opens a fresh connection, unlocks it with that password,
  runs the one call and closes it. There is no "unlocked for 10 minutes"
  window in the browser. Tasks waiting for approval are shown only in the
  answer to the form that carried the password.
- **Pairing** spans several pages (the bind code, the wait, the name), so
  it keeps one connection of its own, unlocked by the password that started
  it. The page only sees a random flow ID; the connection belongs to the
  browser session that started it and is closed when the pairing is
  finished, after 10 minutes, or when that session ends. Naming the new
  device asks for the password again.
- **After every accepted password** the session gets a new cookie and a new
  form token, so copies of the old ones stop working.
- The server stops 30 minutes after the last request, and with the daemon.

### Honest limits

- **Cookies are shared across ports.** Browsers send a cookie for
  `127.0.0.1` to every port on that address, so a local program the browser
  visits on another port can read the session cookie, and one that can set
  cookies there can plant one. With it, that program can do what needs no
  password (kill, pause, disconnect, reject a link, read the pages) until
  the session ends or the cookie rotates. It cannot do anything that needs
  the password, because no unlock is kept between requests.
- Any local process of the same user can run `cravv-connect ui` and use the
  no-password actions, exactly as it could with the CLI. Wrong passwords
  typed on the page count toward the same 15-minute lockout.
- **Launch links.** At most 8 unused launch links are live. When all 8 are
  younger than 10 seconds, `ui.start` is refused as busy ("wait a few seconds
  and run cravv-connect ui again"); otherwise a new link drops the oldest
  unused one. A local process that calls `ui.start` in a loop can therefore
  make a link the human has not opened within 10 seconds stop working; run
  `cravv-connect ui` again.
- **Browser sessions.** At most 8 browser sessions are live and a new one
  ends the oldest. A local process of the same user can open 8 launch links
  of its own and so end the human's browser session (the page then says the
  session ended; run `cravv-connect ui` again). This denies service; it gives
  that process nothing it could not already do over the daemon socket.

## Managed sessions

An offer lets one paired machine start agent sessions on this machine, in
one folder, with nobody at the keyboard. Offers are set per machine with
the password (CLI or web UI), and the rule you set is the approval: a link
request to the offer is accepted at once, at the lower of what was asked
and the offer's permission (`messages` or `tasks-auto`; a request for
`tasks-ask` gets `messages`, because nobody is here to ask). Each item the
peer sends runs `claude -p` in that folder, with the prompt on stdin
(never in argv). What a run can do depends on the offer's run mode:

- **read-only**: it can read, glob and grep inside the folder, and use the
  cravv-connect tools on its one link. Nothing else: no writes, no shell,
  no web. Reads outside the folder, including through symlinks, are refused.
- **edit-in-folder**: it can also edit and write files inside the folder,
  but not the folder's Claude settings, git or tool configuration files.
  No shell, no web.
- **shell**: it can run commands. **A shell run can do anything your user
  can**, including talking to the local cravv-connect daemon without a
  token, reading `~/.cravv-connect`, and editing your `~/.claude`
  settings. Choose it only for a machine you trust as much as yourself.

How a run is contained (Claude Code 2.1.283 flags, checked by probes):

| Run mode | Flags |
|---|---|
| `read-only` (default) | `--permission-mode dontAsk --tools Read,Glob,Grep --allowedTools mcp__cravv-connect__*` |
| `edit-in-folder` | `--permission-mode acceptEdits --tools Read,Glob,Grep,Edit,Write --allowedTools mcp__cravv-connect__*` |
| `shell` | `--permission-mode acceptEdits --tools Read,Glob,Grep,Edit,Write,Bash --allowedTools Bash,mcp__cravv-connect__*` |

Every run also gets `--session-id <uuid>` the first time and
`--resume <uuid>` after, `--output-format json`, `--restricted`,
`--strict-mcp-config --mcp-config <file>`, `--disable-slash-commands`,
`--permission-prompts none` and `--disallowedTools Bash(cravv-connect:*)`,
and never a bypass permission mode. What these do:

- `--restricted`: the user, project and local settings files are ignored,
  so neither your settings nor a `.claude/settings.json` in the folder can
  add hooks, permission rules or a wider permission mode; the file tools
  are confined to the folder; bypassPermissions is refused.
- `--tools` names every built-in tool of the run mode, and the permission
  mode is always given (dontAsk for read-only, acceptEdits otherwise);
  anything that would ask a person is denied.
- Only the cravv-connect MCP server (`--strict-mcp-config` with a config
  the daemon writes), no skills, no CLAUDE.md at any level and no
  auto-memory (`CLAUDE_CODE_DISABLE_CLAUDE_MDS=1`,
  `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`).
- Only an allowlisted environment reaches the run: `HOME`, `PATH`,
  `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_*`, `TMPDIR`, `TERM`, and what
  claude needs to log in and reach the API (`ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_OAUTH_TOKEN`,
  `CLAUDE_CONFIG_DIR` and the proxy variables) when the daemon has them.
  Nothing named `CRAVV_*`.
- The run's MCP server binds to its managed session with a run token that
  works for one connection and one run. It can only send messages, work on
  its own tasks, send files from the offer's folder and list its link. It
  cannot read the inbox (the daemon hands it the item), share, connect,
  decide, unlock, change offers or use machine controls.
- Each run starts in a process group of its own. The group is killed when
  the run ends (even normally), at its timeout, when the session closes,
  on the kill switch and at shutdown, and a daemon that restarts after a
  crash kills groups recorded in the same boot. A process that calls
  `setsid` (or double-forks into a new group) escapes this; only a shell
  run can do that. cravv-connect does not use `systemd-run --scope` on
  Linux.
- Defense in depth: a connection to the daemon from a process inside a
  live run (in its process group, or below its agent in the process tree,
  by the socket's peer PID) may only bind with a run token. So a shell
  run's `cravv-connect status` or a script that talks to the socket is
  refused. A process that escaped the group and was reparented is not
  caught: for a shell run this is a speed bump, not a boundary.
- The folder must be absolute, exist, not be your home folder (also
  through a symlink), and neither contain nor be inside `~/.cravv-connect`.
  The path with symlinks resolved is stored and checked again before every
  new session and every run; a folder that moved closes the session.
- Caps: runs an hour per link (default 30), runs a day per machine
  (default 200, kept across restarts), sessions open at once per offer
  (default 2), a run timeout (default 30 minutes) and an idle timeout
  (default 2 hours). Over a cap a task fails `rate_limited`. The hourly cap
  counts one link, and a managed session has one link, so a peer that
  closes it and starts a new managed session starts a new hourly count;
  the daily cap and the open sessions cap still hold.
  `max_turns_per_run` is recorded but not passed, because Claude Code does
  not document `--max-turns`.
- Every run is audited (`managed_run`: session, link, outcome, exit code,
  duration, turns). A managed session has exactly one link; closing the
  link, removing the offer, unpairing the machine, the idle timeout and the
  kill switch close it.

**Opening a managed session.** `cravv-connect session open <name>` (or
Open in the web UI) resumes the conversation in your terminal with your
normal Claude settings, hooks and permissions, and holds the session's
queue until you exit, so a run never races you. It was written by a run the
peer drove, so both warn: "This conversation was driven by <machine>. It
opens with your normal Claude settings; review before continuing."

**Transcripts.** Claude Code keeps every managed conversation under
`~/.claude/projects/<folder path with dashes>/<session id>.jsonl`. They
grow with every run and stay after the session closes. cravv-connect does
not delete them; remove old ones by hand when you no longer need them.

## What agents can and cannot do

Through the MCP tools a chat agent can:

- share its chat as a session, close it or change its purpose and
  visibility; list paired machines, the sessions and offers a machine
  shows, and its own links;
- ask for links (`connect`), which a human on the other machine decides;
- send messages, tasks and files on its own session's active links, read
  its own inbox, and work on tasks delivered to its session;
- ask its human to decide requests and held tasks (`review_pending`);
- restrict or disconnect its own links, close its session, and pull the
  kill switch (again, if it is already on).

It cannot:

- see or act on another session's links, inbox or tasks (another session's
  items look missing);
- accept a link or approve a task without its human (a form or a code the
  model never sees), grant `tasks-auto`, raise a link, edit offers, pair,
  resume after the kill switch, allow new folders or reset the identity;
- send files from outside its session's project folder and the folders the
  human allowed. The session's folder is the agent process's working
  directory, which the agent chooses, so it is not a boundary the human
  sets: keep agents that must not read your files away from this tool;
- send any hidden file or folder (any path part starting with a dot), or
  files whose names look like secrets: `.env*`, `id_*`, `credentials*.json`,
  `service-account*.json`, and `*.pem`, `*.key`, `*.env`, `*.p12`, `*.pfx`,
  `*.jks`, `*.keystore`, `*.kdbx`, `*.ppk`, `*.ovpn` (case-insensitive). Only
  regular files up to 100 MB with a single hard link are sent. Symlinks are
  resolved and checked again, and the file is opened without following links
  and must be the file that was checked. Every uploaded file is written to
  the audit log with the peer, path, size and SHA-256.

An agent that can run shell commands can also run the `cravv-connect` CLI
like any local process, and so use the no-password commands (pause,
unpair, reject, disconnect, kill). Claude Code's allow rules that
`cravv-connect install claude` adds cover only the MCP tools that act
within an existing link or only read, and the listener; `connect`,
`create_task` and `send_file` still ask unless you install with
`--allow-send`.

`cravv-connect kill` (or the agent's `kill_switch`) is saved first and
survives restarts. It stops managed runs (their process groups), fails the
tasks claimed here as `killed`, closes every link and tells the peers, and
tries for 3 seconds to send those updates while still connected. The switch
counts as on from the start of that window: agent calls are refused and
incoming frames are left unhandled. The daemon then stops file transfers,
disconnects from the relay, and stops handling incoming frames, which wait
at the relay for up to 7 days. Until `cravv-connect resume` (password),
every IPC method fails except `status`, `peer.list`, `machines`,
`audit.read`, `hook.counts`, `auth.unlock`, `resume`, `kill`,
`reset_identity`, `daemon.shutdown`, `offers.list`, `managed.list`,
`managed.close`, `sessions.local` and `ui.start` (so the web UI can resume).
After resume, links must be requested again. `reset-identity` still works
(with the password), so a machine killed because it may be compromised gets
a new identity without reconnecting under the old one first.

## Keys and local storage

- **State directory:** `~/.cravv-connect` (or `CRAVV_HOME`), mode `0700`.
  `config.toml`, `store.db`, `audit.log`, `daemon.sock`, `daemon.pid` and
  the daemon logs are `0600`; `files/`, `wake/` and `runs/` and their
  folders are `0700`, and the files in them `0600`.
- **Identity key (Ed25519 seed):**
  - macOS: a generic password (service `cravv-connect`, account `identity`,
    base64 seed) in the default keychain, normally the login keychain,
    written through `security(1)`. The seed is passed to `security` as an
    argument, so another process of the same user could see it in the process
    list for a moment. If the Keychain refuses the write, the seed goes to the
    fallback below; if the Keychain cannot be read and there is no fallback
    copy, the daemon refuses to start instead of creating a new identity.
    Every `security` call has a 10 second limit: a locked Keychain can wait
    for an unlock dialog a background daemon never shows, and a timeout
    stops the start with an error (and never writes the fallback).
  - Linux (and the macOS fallback): the `settings` table of `store.db`,
    base64 encoded, protected only by file permissions, so any process of
    the same user can read it.
- **Prekeys:** private X25519 keys in `store.db`. The current one is rotated
  every 7 days (not while the kill switch is on), and superseded ones are
  deleted 21 days after they were replaced, which bounds how much past
  traffic a stolen `store.db` can open.
- **Tokens:** `store.db` holds only the SHA-256 of wake and reattach tokens;
  run tokens are kept (hashed) in memory only.
- **Messages and files** are stored in plaintext locally: the inbox in
  `store.db` (30 days), sent messages in the outbox (up to 21 days), tasks
  with their text and results (kept until you delete the store), sessions
  and links (closed ones deleted after 30 days), managed-session offers and
  run starts, and received files under `files/<alias>/` (never deleted
  automatically). The relay admin token given to `init --relay-token` (or
  typed into `setup`) is kept in `store.db` until the first successful
  registration.
- `cravv-connect reset-identity` (also while killed) turns the kill
  switch on (or leaves it on), deletes every peer, prekey and queued outgoing
  message, and replaces the identity. The new identity has no relay mailbox:
  it registers again with an admin token or with the invite received the
  next time you join a pairing. Every peer has to pair again; run
  `cravv-connect resume` when ready.

## Audit log

`~/.cravv-connect/audit.log` is append-only JSON lines (`cravv-connect log`
shows it). It records pairing and unpairing, pause and resume, kill and
resume, every password attempt, link requests, acceptances, rejections,
closes and permission changes, task approvals and denials, every incoming
task, managed-session offers set and removed, managed sessions started,
opened, closed and refused, every managed run, allowed folders, identity
resets, every accepted file, every completed download and every outgoing
file, with a timestamp and, where they apply, the local alias and machine
ID, the item ID and a content hash. Agents can read it with
`cravv-connect log`.

It is **not tamper-evident**: any process running as your user can edit or
delete it. Treat it as a record for you, not as evidence.

The daemon's operational log (`daemon.log`, written with `daemon run
--log-file`, which the login services and `daemon start` use) is separate
from the audit log. It rotates at 10 MiB and keeps 3 old files
(`daemon.log.1` to `.3`), so it cannot fill the disk; `daemon-stderr.log`
only catches crash output.

## What the relay sees

The relay operator (or anyone who compromises it) can see:

- every machine's identity public key and mailbox ID, and its IP address when
  it connects;
- each mailbox's allow-list, so who is paired with whom;
- which member minted each invite and which new mailbox used it, so who
  introduced whom;
- when each machine sends and receives, the size of every frame, and message
  IDs (which contain a millisecond timestamp). This includes the presence
  pings two machines exchange every 30 seconds while they have an active
  link, so the relay can tell when two machines have links open (not how
  many, or between which sessions), and the timing of discovery requests;
- the prekey ID in each frame header, and so when each machine rotates
  prekeys;
- pairing nameplates, which member created each room, the joiner's IP address
  and when rooms are used (never the secret part of the code);
- blob sizes, chunk counts, uploader and recipient keys, and download times.

It cannot see message contents, task text, file contents, file names, aliases,
device names, session names or purposes, link IDs or notes, offer labels,
or tokens: all of those are inside encrypted frames or the encrypted
pairing payload. The e2e test `TestAcceptance_8_SecurityHolds` records
everything a relay stores during pairing, discovery, a link request, chat,
a task and a close, and checks that none of these appear.

## Reporting a vulnerability

Please report security problems privately: open a private security advisory
on the project's repository, or contact its maintainers directly. Do not open
a public issue for an unfixed vulnerability. Include the version or commit,
your platform, and steps to reproduce. For a relay deployment you operate,
also rotate the relay admin token (`wrangler secret put ADMIN_TOKEN` for the
Cloudflare relay, or restart `cravv-relay` with a new
`CRAVV_RELAY_ADMIN_TOKEN`) if you suspect it leaked.
```

- [ ] **Step 2: Check it**

```sh
grep -n '^## ' docs/security.md
grep -c $'\u2014' docs/security.md
```

Expected: the sections The model, What we defend against, What we do not defend against, Tokens and session identity, The password gate, The web UI, Managed sessions, What agents can and cannot do, Keys and local storage, Audit log, What the relay sees, Reporting a vulnerability (each once), and `0`.

- [ ] **Step 3: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 4: Commit**

```sh
git add docs/security.md
git commit -F - <<'MSG'
docs: security for v2 (links and the link gate, tiered decisions, tokens, managed sessions and their containment, honest limits, what the relay sees)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 9: docs: agents for v2

`docs/agents.md` described the v1 tools and hooks. It now lists the v2 tools and which ones `install claude` allows, the Claude Code install (MCP server, hooks, allow rules, `--allow-send`, the `/cravv` skill) and its manual equivalent, sharing a chat and the wake-file listener, deciding in the chat (forms in the terminal, codes for the VS Code extension, the password path on Linux), the hooks and their chat-ID limitation after `/clear`, Codex and the other MCP clients (`wait_for_message` polling, the web UI for approvals), managed sessions from the requesting side (`new:<label>`, `session open|close|list`, `CRAVV_CLAUDE`), and agents without MCP: the v1 JSON commands are gone and their link-scoped replacement is deferred, so such agents speak ipc-v1 directly and use `cravv-connect listen`.

**Files:**
- Modify: `docs/agents.md`

**Interfaces:**

Documents: `mcpserver.Tools`, `install.Claude` (`InstallWith`, `claudeAllowRules`, `CravvSkill`, hooks), `install.Codex`, `cli` `listen`, `hook`, `mcp`, `daemon.FindClaude`.

- [ ] **Step 1: Rewrite the document**

Replace the whole of `docs/agents.md` with:

````markdown
# Connecting your coding agent

cravv-connect talks to agents through a stdio MCP server, `cravv-connect mcp`.
Every agent that can start an MCP server can use it. A chat takes part by
sharing itself as a session; sessions on paired machines then ask to link
with it, and everything travels over those links.

Before you start, set the machine up with `cravv-connect setup` (or
`cravv-connect setup --join <code>` on a second machine): it installs the
daemon and offers the Claude Code and Codex integrations below. Use the full
path of the binary in the snippets below if it is not on the `PATH` your
agent sees; `which cravv-connect` prints it.

The MCP server takes its project folder from the directory the agent starts
it in. If your editor starts MCP servers somewhere else, add
`"--project-dir", "<absolute project path>"` after `"mcp"`; VS Code and
Cursor accept `${workspaceFolder}` there. Files a chat sends must be inside
that folder or a folder you allowed with `cravv-connect allow-path`.

## The tools

| Tool | What it does | Claude Code, after `install claude` |
|---|---|---|
| `session_share(name, purpose, visibility?)` | Share this chat as a session; returns the listener command | allowed |
| `session_close()` | Close this chat's session and all its links | allowed |
| `session_set(purpose?, visibility?)` | Change the purpose or who may see it (`private`, `all-peers`, `peers:<alias>,...`) | allowed |
| `machines()` | Paired machines, online or paused | allowed |
| `sessions(machine)` | The sessions and managed-session offers a machine shows this one | allowed |
| `connect(target, permission, note?)` | Ask `machine/session`, or `machine/new:<offer label>`, for a link; the other side decides | asks |
| `links()` | This session's links: peers, permissions, state, presence | allowed |
| `disconnect(link)` | Close a link | allowed |
| `restrict(link, permission)` | Lower what the other side may do here | allowed |
| `check_inbox(limit?)` | What arrived for this session, wrapped in `<remote_message>` | allowed |
| `wait_for_message(timeout_s?)` | Block until something arrives (default 50 seconds, at most 600) | allowed |
| `review_pending(item?, decision?, code?)` | Ask your human to decide link requests and held tasks | allowed |
| `send_message(link, text)` | Chat on a link | allowed |
| `create_task(link, instructions, file_paths?)` | Give the other session a task | asks |
| `get_task`, `claim_task`, `update_task`, `complete_task`, `fail_task`, `cancel_task` | Work on tasks; peer text comes only in the `wrapped` field | allowed |
| `send_file(link, path)` | Send a file from the project folder (up to 100 MB; hidden and secret files refused) | asks |
| `kill_switch()` | Stop everything until the human resumes with the password | asks |

`permission` is `messages` (chat and files), `tasks-ask` (tasks that each
need a human decision on the other side) or `tasks-auto` (tasks the other
agent may run without asking). Machine management (pairing, pausing,
unpairing, offers) is for the human, in the CLI or the web UI.

## Claude Code

### Install

```sh
cravv-connect install claude            # or: cravv-connect setup
cravv-connect install claude --allow-send
```

This:

- registers the MCP server: `claude mcp add --scope user cravv-connect -- <bin> mcp`;
- adds the UserPromptSubmit and Stop hooks (`<bin> hook`, 5 second
  timeout) to `~/.claude/settings.json`;
- adds allow rules to `permissions.allow` there for every tool marked
  "allowed" above, plus `Bash(cravv-connect listen:*)` and
  `Bash(<bin> listen:*)` (the listener command uses the plain name when
  the `PATH` finds this binary, else its full path), so the listener and the tools that only read or act within an existing link
  never stop the chat for a permission prompt;
- writes the `/cravv` skill to `~/.claude/skills/cravv/SKILL.md`.

`connect`, `create_task` and `send_file` open new flows or send local files,
so they still ask. `--allow-send` allows them too; `--no-allow-send` removes
the rules `--allow-send` added. Running it again without either flag keeps
the send rules as they are. Rules and skills you wrote yourself are never
changed, and `cravv-connect uninstall claude` removes exactly what it added.
Restart Claude Code afterwards.

Manual equivalent:

```sh
claude mcp add --scope user cravv-connect -- /usr/local/bin/cravv-connect mcp
```

and in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "/usr/local/bin/cravv-connect hook", "timeout": 5 } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "/usr/local/bin/cravv-connect hook", "timeout": 5 } ] }
    ]
  },
  "permissions": {
    "allow": [
      "mcp__cravv-connect__machines", "mcp__cravv-connect__sessions", "mcp__cravv-connect__links",
      "mcp__cravv-connect__check_inbox", "mcp__cravv-connect__wait_for_message", "mcp__cravv-connect__review_pending",
      "mcp__cravv-connect__session_share", "mcp__cravv-connect__session_close", "mcp__cravv-connect__session_set",
      "mcp__cravv-connect__disconnect", "mcp__cravv-connect__restrict", "mcp__cravv-connect__send_message",
      "mcp__cravv-connect__get_task", "mcp__cravv-connect__claim_task", "mcp__cravv-connect__update_task",
      "mcp__cravv-connect__complete_task", "mcp__cravv-connect__fail_task", "mcp__cravv-connect__cancel_task",
      "Bash(cravv-connect listen:*)", "Bash(/usr/local/bin/cravv-connect listen:*)"
    ]
  }
}
```

### Share a chat: `/cravv`

Type `/cravv` in a chat (or ask "share this chat with cravv-connect"). The
skill asks for a short name (`a-z`, `0-9`, `-`), a one-line purpose and who
may see it, then:

1. calls `session_share`, which returns a listener command such as
   `cravv-connect listen --wake-file /Users/you/.cravv-connect/wake/wake-<32 hex>`;
2. runs it as a background command (the Bash tool with
   `run_in_background`). The command names a private file that holds the
   wake token; the token itself never appears in the command, the chat
   transcript or `ps`.

The listener blocks until something arrives for this chat's session, prints
one line naming only the local machine alias and link number, and exits:

```text
cravv-connect: 1 new task on link 2 from gpu-box. Call check_inbox.
cravv-connect: 1 link request on link 3 from laptop. Call check_inbox, then review_pending.
```

Claude Code wakes an idle chat when a background command exits, so the
chat handles what arrived without you asking: `check_inbox`, then
`review_pending` when the line says so, then it starts the listener again.
If the chat forgets to re-arm it, the prompt hook adds "The listener for
this chat's session is not running" to your next prompt, and the Stop hook
keeps the chat going while something unhandled is waiting, so delivery
falls back to "next turn", never to silence.

To link with a session on another machine, ask in plain words ("connect to
gpu-box/trainer so it can run tasks for us"); the chat calls `sessions` and
`connect`. The human on that machine decides.

### Accept and reject in the chat

Link requests to this chat and tasks on its `tasks-ask` links wait for you.
The chat calls `review_pending`, and:

- **In a terminal (Claude Code CLI):** a form appears in the chat. Choose
  accept, a lower level, or reject. The model never answers it; a form you
  dismiss decides nothing.
- **In the VS Code extension:** the extension declines forms without
  showing them, so the daemon shows a desktop notification on macOS with a
  4-digit code. Type `accept 4821` (or `reject`) in the chat; the chat
  passes what you typed to `review_pending`. The model never sees the
  code. A code works once, for 10 minutes, and 3 wrong ones lock that item.
- **Where neither works** (Linux has no notifier): `review_pending` names
  the password path, `cravv-connect link accept <link>` or
  `cravv-connect approvals` in a terminal, or the web UI
  (`cravv-connect ui`).

A chat can accept a link at `messages` or `tasks-ask` and approve single
tasks. Granting `tasks-auto`, raising a link and editing offers always need
your password (`cravv-connect link accept`, `link permit`, or the web UI).
Tasks that reach the chat were approved by a human (or arrived on a
`tasks-auto` link), and the MCP instructions tell the agent not to ask
again.

### Hooks

- **UserPromptSubmit** prints one line such as
  `cravv-connect: 2 new messages on link 2 from gpu-box. Call check_inbox.`
  and Claude Code adds it to your prompt.
- **Stop** answers `{"decision":"block","reason":"<the same line>"}` when
  this chat's session has unhandled items, so Claude takes one more turn.
  It never blocks for decisions alone (only you can make those), not again
  for the same items while `stop_hook_active`, and at most twice in a row.
- Both find the chat's own session through Claude Code's chat ID
  (`CLAUDE_CODE_SESSION_ID`, given to the MCP server, and `session_id`,
  given to hooks), so two chats in one folder are not confused. After
  `/clear` the chat ID changes but the MCP server keeps the old one, so
  the hooks stay silent for that chat; the listener keeps working. A hook
  whose chat ID is unknown answers for the newest open session of its
  folder that no chat claims.
- They never print message bodies or names chosen by the other machine,
  and print nothing when the daemon is not running.

## Codex

```sh
cravv-connect install codex             # or: cravv-connect setup
```

This adds (or rewrites) one table in `~/.codex/config.toml` and leaves the
rest of the file alone:

```toml
[mcp_servers.cravv-connect]
command = "/usr/local/bin/cravv-connect"
args = ["mcp"]
```

Codex has no hooks and no `/cravv` skill. Ask it to share the chat
(`session_share`), then to listen with `wait_for_message`, calling it again
while you wait. The default wait is 50 seconds, which fits clients whose
tool calls time out after a minute; pass `timeout_s` up to 600 only if your
client allows longer tool calls. For decisions it calls `review_pending`:
if the client shows no form, you get the code notification on macOS;
otherwise decide in the web UI (`cravv-connect ui`, Approvals page) or the
terminal.

## Cursor

Project level, `.cursor/mcp.json` (or `~/.cursor/mcp.json` for every project):

```json
{
  "mcpServers": {
    "cravv-connect": {
      "command": "/usr/local/bin/cravv-connect",
      "args": ["mcp", "--project-dir", "${workspaceFolder}"]
    }
  }
}
```

## VS Code (GitHub Copilot agent mode)

`.vscode/mcp.json` in the workspace. Note the top-level key is `servers`:

```json
{
  "servers": {
    "cravv-connect": {
      "type": "stdio",
      "command": "/usr/local/bin/cravv-connect",
      "args": ["mcp", "--project-dir", "${workspaceFolder}"]
    }
  }
}
```

## Gemini CLI

`~/.gemini/settings.json` (or `.gemini/settings.json` in a project):

```json
{
  "mcpServers": {
    "cravv-connect": {
      "command": "/usr/local/bin/cravv-connect",
      "args": ["mcp"]
    }
  }
}
```

Cursor, Copilot and Gemini CLI work like Codex: share the chat, listen with
`wait_for_message`, and decide in the chat's forms, with a code, or in the
web UI.

## Managed sessions

A machine can also run sessions for another machine with nobody there.
On the machine that runs them, the human makes an offer (password):

```sh
cravv-connect offers set mac trainer --folder ~/work/wakeword --permission tasks-auto --mode edit-in-folder
cravv-connect offers list
```

The other machine's chat then sees the offer in `sessions("gpu-box")` and
calls `connect("gpu-box/new:trainer", "tasks-auto")`. The daemon starts a
managed session named `trainer-<4 characters>` and accepts at once; each
message or task runs `claude -p` in the folder, and the answers come back
over the link. Run modes, containment and limits are in
[security.md](security.md#managed-sessions).

- `cravv-connect session list` shows managed sessions,
  `cravv-connect session open <name>` continues one's conversation in your
  terminal (its queue waits until you exit; it warns that a peer drove it),
  and `cravv-connect session close <name>` closes it and its link.
- The daemon looks for `claude` in `$CRAVV_CLAUDE`, then the `PATH`, then
  `~/.local/bin/claude`, `~/.claude/local/claude`,
  `/opt/homebrew/bin/claude` and `/usr/local/bin/claude`. A daemon started
  by launchd or systemd has a short `PATH`, so set `CRAVV_CLAUDE` in its
  service environment if `claude` lives somewhere else.
- Only Claude Code can run managed sessions in this version.

## Agents without MCP

The v1 JSON commands (`cravv-connect send`, `inbox`, `wait`, `task ...`)
were removed in v2, because they acted for the whole machine. Their
link-scoped replacement (a CLI-held session) is not built yet. Until then:

- a program can speak [ipc-v1](../protocol/ipc-v1.md) on the daemon socket
  itself: `session.register`, `session.share` and the link methods, on one
  connection it keeps open (the session belongs to that connection);
- `cravv-connect listen` works for any program: pass the wake token from
  `session.share` on stdin, or in a file only you can read with
  `--wake-file`.

## What agents can never do

Accepting a link or approving a task without a human (a form or code the
model never sees, or your password), granting `tasks-auto`, raising a link,
editing offers, pairing and joining, resuming after the kill switch,
allowing new folders and resetting the identity all need a human. No agent
tool accepts a password. Type it only into the `cravv-connect` CLI in your
own terminal or the web UI `cravv-connect ui` opened; if an agent or a
message asks you for it anywhere else, do not type it.
````

- [ ] **Step 2: Check it**

```sh
grep -c 'trust\|cravv-connect send\|cravv-connect wait' docs/agents.md
grep -c $'\u2014' docs/agents.md
```

Expected: `1` (the sentence saying the v1 JSON commands `cravv-connect send`, `inbox`, `wait` were removed) and `0`.

- [ ] **Step 3: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 4: Commit**

```sh
git add docs/agents.md
git commit -F - <<'MSG'
docs: agents for v2 (the tools, Claude Code install, /cravv, the listener, decisions in chat and codes, hooks; Codex and other MCP clients; managed sessions; agents without MCP)

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```

---

### Task 10: docs: README for v2, with tests that keep the CLI reference complete and every doc free of em dashes

The README is rewritten for v2: what it is (session-to-session links, the chat as the hub), install (the one-liner, glibc, the attestation, from source), the quick start around `setup` and `setup --join` with a real transcript, using it from a chat (`/cravv`, the listener, connect and the three levels, deciding in the chat, codes in VS Code), managed sessions (offers, run modes and their risk, open), the web UI, the cut-off controls, the CLI reference, troubleshooting for v2 and development. `TestREADMEDocumentsEveryCommand` walks the real command tree (`cli.NewRoot`) and requires every runnable command in a CLI reference row with every flag it takes, and every command a row names to exist. `TestDocsHaveNoEmDashes` checks every Markdown file in the repository. The existing README checks (`TestReadmeQuickStart`, `TestReadmeStatesGlibc`, `TestReleaseWorkflowAttests`) keep passing.

**Files:**
- Create: `e2e/docs_test.go`
- Modify: `README.md` (rewritten)
- Test: `e2e/docs_test.go`; `scripts/readme_test.go` and `cmd/cravv-connect/release_test.go` (unchanged)

**Interfaces:**

Consumes: `cli.NewRoot`, `cli.Env`, `github.com/spf13/cobra`, `github.com/spf13/pflag`.

Produces: `TestREADMEDocumentsEveryCommand`, `TestDocsHaveNoEmDashes`.

- [ ] **Step 1: Write the failing tests**

Create `e2e/docs_test.go`:

```go
package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/cookwithcravv/cravv-connect/internal/cli"
)

// readmeCLIReference returns the rows of the README's CLI reference table.
func readmeCLIReference(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "\n## CLI reference\n")
	if !ok {
		t.Fatal("README has no CLI reference section")
	}
	var rows []string
	started := false
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| `") {
			rows = append(rows, line)
			started = true
		} else if started && !strings.HasPrefix(line, "|") {
			break
		}
	}
	if len(rows) == 0 {
		t.Fatal("the CLI reference has no rows")
	}
	return rows
}

// cliCommands returns every command a person can run, by its path without
// the program name ("link accept"), from the real command tree.
func cliCommands() map[string]*cobra.Command {
	out := map[string]*cobra.Command{}
	var walk func(prefix string, c *cobra.Command)
	walk = func(prefix string, c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden {
				continue
			}
			path := strings.TrimSpace(prefix + " " + sub.Name())
			if sub.Runnable() {
				out[path] = sub
			}
			walk(path, sub)
		}
	}
	walk("", cli.NewRoot(&cli.Env{}))
	return out
}

var (
	codeSpan    = regexp.MustCompile("`([^`]+)`")
	commandWord = regexp.MustCompile(`^[a-z][a-z-]*$`)
)

// rowCommands returns the command words at the start of each code span in
// a row's first cell ("link permit" from "`link permit <link> <level>`").
func rowCommands(row string) []string {
	cells := strings.Split(row, " | ")
	var out []string
	for _, m := range codeSpan.FindAllStringSubmatch(cells[0], -1) {
		var words []string
		for _, w := range strings.Fields(m[1]) {
			if !commandWord.MatchString(w) {
				break
			}
			words = append(words, w)
		}
		out = append(out, strings.Join(words, " "))
	}
	return out
}

// The README's CLI reference names every command a person can run, with
// every flag it takes, and nothing that does not exist.
func TestREADMEDocumentsEveryCommand(t *testing.T) {
	t.Parallel()
	rows := readmeCLIReference(t)
	cmds := cliCommands()
	documented := map[string][]string{} // command path -> the rows naming it
	for _, row := range rows {
		for _, words := range rowCommands(row) {
			path, found := words, false
			for path != "" {
				if _, ok := cmds[path]; ok {
					documented[path] = append(documented[path], row)
					found = true
					break
				}
				path = strings.TrimSpace(path[:max(strings.LastIndex(path, " "), 0)])
			}
			if !found {
				t.Errorf("the CLI reference names %q, which is not a command", words)
			}
		}
	}
	for path, c := range cmds {
		rs, ok := documented[path]
		if !ok {
			t.Errorf("the CLI reference does not document `%s`", path)
			continue
		}
		text := strings.Join(rs, "\n")
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			if !strings.Contains(text, "--"+f.Name) && (f.Shorthand == "" || !strings.Contains(text, "-"+f.Shorthand+" ") && !strings.Contains(text, "-"+f.Shorthand+"]")) {
				t.Errorf("the CLI reference for `%s` does not mention --%s", path, f.Name)
			}
		})
	}
}

// No Markdown file in the repository has an em dash.
func TestDocsHaveNoEmDashes(t *testing.T) {
	t.Parallel()
	var checked int
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, "\u2014") {
				t.Errorf("%s:%d has an em dash: %s", path, i+1, line)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 5 {
		t.Fatalf("checked only %d Markdown files", checked)
	}
}
```

```sh
go test ./e2e -run 'TestREADMEDocumentsEveryCommand|TestDocsHaveNoEmDashes' -count=1 2>&1 | grep -c 'is not a command\|does not document'
```

Expected: `23`, the failures of `TestREADMEDocumentsEveryCommand` against the v1 README: it names `trust`, `send`, `inbox`, `wait` and `task ...`, which are not commands, and does not document `links`, `link accept|reject|permit`, `sessions`, `offers ...`, `session ...`, `listen` or `ui`. `TestDocsHaveNoEmDashes` already passes.

- [ ] **Step 2: Rewrite the README**

Replace the whole of `README.md` with:

````markdown
# cravv-connect

cravv-connect links AI coding agent chats on different machines. A Claude
Code chat on your laptop and a chat on a GPU box can send each other
messages, hand each other tasks and get the results back, and send files,
all end-to-end encrypted through a relay you deploy.

- **Session-to-session links.** A chat shares itself as a named session
  (`mac/training`), and a session on a paired machine asks to link with it.
  Everything travels over an accepted link, and nothing on one link is ever
  visible to another session. A chat may hold several links.
- **The chat is the hub.** What arrives for a chat wakes it, even while it
  is idle, and you accept or reject requests and tasks right in the chat.
- **You decide what each link allows**: messages only, tasks you approve
  one by one, or tasks the other agent may run without asking.
- **Managed sessions.** A machine can let a paired machine start agent
  sessions in a folder you chose, with nobody at the keyboard.
- **Simple setup** with an install one-liner and one command per machine,
  no Go toolchain, and a local web page for everything you decide or watch.
- Pairing, wide grants and managed-session rules need your login password,
  so no agent (local or remote) can open them up on its own. Any link or
  machine can be cut off at once, without a password, and there is a kill
  switch.
- The relay only ever sees ciphertext and routing metadata.

Read [docs/security.md](docs/security.md) before you rely on it. In short:
cravv-connect stops a peer or an injected prompt from pairing, accepting
links, granting automatic tasks or editing rules, but a message can still
try to talk your agent into something within its own permissions. Keep your
agent's permission settings strict.

**Type your login password only into the `cravv-connect` CLI in your own
terminal, or into the local web page `cravv-connect ui` opened.**

## How it works

```
 Mac                                            GPU box
 chat "training" --MCP--+                  +--MCP-- chat "trainer"
 chat "voice"    --MCP--+-> daemon    daemon <-+--MCP-- chat "wakeword"
                            |            |
                            +--> relay <-+
                   (WebSocket + HTTPS, only ciphertext)

 links: mac/training <-> gpu-box/trainer, mac/voice <-> gpu-box/wakeword
```

One Go binary, `cravv-connect`, is the daemon (the only part that uses the
network), the MCP server (`cravv-connect mcp`), the listener
(`cravv-connect listen`), the hook (`cravv-connect hook`), the web UI and
the CLI. The relay is either the Cloudflare Worker in
[`relay-cf/`](relay-cf/README.md) for real use, or the Go reference relay
`cravv-relay` for local testing. Protocols: [relay-v1](protocol/relay-v1.md),
[peer-v1](protocol/peer-v1.md), [ipc-v1](protocol/ipc-v1.md).

## Install

On macOS or Linux (arm64 or amd64). The Linux binaries need
glibc 2.35 or newer (Ubuntu 22.04, Debian 12, Fedora 36 and later); on an
older or a musl system such as Alpine, install from source.

```sh
curl -fsSL https://raw.githubusercontent.com/cravv/cravv-connect/main/scripts/install.sh | sh
```

This downloads the latest release, checks it against the release's
`SHA256SUMS`, and installs `cravv-connect` and `cravv-relay` to
`~/.local/bin` (it tells you if that folder is not on your `PATH`). No Go
toolchain is needed. Use `sh -s -- --system` to install to `/usr/local/bin`
instead, and `CRAVV_VERSION=v1.2.0` to pick a release. The repository URL is a
placeholder until the project is published.

Every release archive also has a signed build provenance attestation from the
release workflow. To check that an archive you downloaded was built there, use
the [GitHub CLI](https://cli.github.com/):

```sh
gh attestation verify cravv-connect_1.2.0_linux_amd64.tar.gz --repo cravv/cravv-connect
```

### Install from source

You need Go 1.26 and a C toolchain, because password checks use PAM through
cgo:

- **macOS:** the Xcode command line tools (`xcode-select --install`).
- **Linux:** gcc and the PAM headers (`sudo apt install build-essential libpam0g-dev`
  on Debian and Ubuntu, `sudo dnf install gcc pam-devel` on Fedora).

```sh
git clone <this repository> cravv-connect
cd cravv-connect
make build            # bin/cravv-connect (cgo), bin/cravv-relay, bin/cravv-conformance
sudo install bin/cravv-connect /usr/local/bin/
```

A binary built with `CGO_ENABLED=0` works for everything except the actions
that need your password; those fail with "password check unavailable: this
cravv-connect binary was built without PAM support". The daemon does the
check, so the binary the daemon runs is the one that needs cgo.

## Quick start

### 1. Set up the first machine

```sh
cravv-connect setup
```

The wizard walks through:

1. **Relay.** Enter your relay URL (for real use, deploy the Cloudflare relay
   once: [relay-cf/README.md](relay-cf/README.md)), or press Enter to start a
   LAN test relay on this machine. The test relay is `cravv-relay` from next
   to `cravv-connect`, on port 8787 at `http://<host>.local:8787` when that
   name resolves (otherwise the machine's network address). It keeps
   everything in memory, stops when the machine restarts, and anyone on the
   network can reach it.
2. **Admin token**, only if this is the relay's first machine (setup makes
   one for a test relay it starts).
3. **This machine and the daemon.** It writes `~/.cravv-connect/config.toml`,
   installs the daemon as a login service (launchd on macOS, systemd on
   Linux) and waits until it is connected to the relay.
4. **Agents.** It detects Claude Code and Codex and offers to add
   cravv-connect to each (see [docs/agents.md](docs/agents.md)).
5. **Pair a device.** It asks for your login password, shows a join code
   and its QR code, and waits for the other machine.

```text
== Pair a device ==
Join code: cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A

On a new machine run:
  cravv-connect setup --join cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A
On a machine already set up for this relay run:
  cravv-connect join CRAVV-7K3F-9QXM-TR2A
The code works once and expires in 10 minutes.
Waiting for the other machine...
```

Run `cravv-connect setup` again at any time: it shows what is set up and
offers only the missing steps. `cravv-connect setup --reset` starts over (it
asks first). Without questions:
`cravv-connect setup --yes --relay <url> [--relay-token <token>] [--name <name>] [--no-agents]`.

### 2. Set up every other machine

Install as above, then use the join code the first machine shows:

```sh
cravv-connect setup --join cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A
```

It shows the relay in the code and asks `Join relay https://relay.example.com? (y/N)`.
It refuses a plain `http` relay unless the address is this machine or a
private network, and it refuses a machine already set up for another relay
(`cravv-connect setup --reset --join <code>` moves it; you pair again with
every peer). Then it sets the machine up, joins (your login password), asks
for a local name for the other machine, and offers the agents. Both machines
are now paired: each can see the sessions the other shares with it and ask
for links, and nothing else.

A join code works once and expires in 10 minutes. To pair two machines that
are already set up, run `cravv-connect pair` on one and
`cravv-connect join <code>` on the other (the join code or the plain bind
code).

### 3. Use it from Claude Code

Restart Claude Code and type `/cravv` in a chat. The chat is shared as a
session, and sessions on paired machines can ask to link with it; you decide
each link.

## Using it from a chat

These steps are for Claude Code; Codex and other MCP clients work the same
way with `wait_for_message` instead of the listener
([docs/agents.md](docs/agents.md)).

**Share this chat.** `/cravv` asks for a short name, a one-line purpose and
who may see it (`private`, `all-peers` or `peers:<alias>`), shares the chat
and starts a background listener. The listener exits with one line when
something arrives, such as
`cravv-connect: 1 new task on link 2 from gpu-box. Call check_inbox.`, and
Claude Code wakes the idle chat, which reads it and starts the listener
again. If it forgets, your next prompt says so.

**Connect.** Ask in plain words: "connect to gpu-box/trainer so it can run
tasks for us". The chat lists what `gpu-box` shows it and asks for a link
at a level:

| Level | The other side may |
|---|---|
| `messages` | Chat and send files |
| `tasks-ask` | Also send tasks; a human on the receiving side approves each one |
| `tasks-auto` | Also send tasks its agent may carry out without asking |

The human on the other machine decides. Each side sets what the other may
do to it: on the asking side a new link lets the other session send
messages only, until you raise it (`cravv-connect link permit`, password).

**Accept or reject in the chat.** When a link request or a `tasks-ask`
task arrives, the chat calls `review_pending`:

- In Claude Code in a terminal, a form appears in the chat: accept, accept
  lower, or reject. The model never answers it.
- The VS Code extension does not show forms, so on macOS a desktop
  notification shows a 4-digit code. Type `accept 4821` (or `reject`) in
  the chat. The model never sees the code.
- Where neither works (Linux has no notifier), decide with
  `cravv-connect link accept <link>` or `cravv-connect approvals` in a
  terminal, or in the web UI.

The chat can accept at `messages` or `tasks-ask` and approve single tasks.
Granting `tasks-auto`, raising a link and editing offers need your
password. A task is decided once; the sender sees its state throughout,
including `seen` (the receiving chat has read it), so a slow chat and a
stuck one look different.

**Stop.** Ask the chat to disconnect a link, restrict it, or close its
session (`session_close` closes all its links; the other side learns within
seconds). `kill_switch` stops everything.

Everything that arrives is wrapped, and agents are told never to treat it as
your instructions:

```
<remote_message from="gpu-box" session="trainer" link="2" permission="tasks-auto" id="01J..." kind="task" task_id="01J...">
...escaped body...
</remote_message>
```

`from` is your name for the machine, `link` your link number, and
`permission` what that link lets the other session do here. The daemon only
accepts IDs in its own format, and the CLI and the web UI strip control,
bidi and invisible characters from anything a peer chose before showing it.

## Managed sessions

A machine can run sessions for a paired machine with nobody there, for
example so a Mac chat can drive training runs on a GPU box. On the GPU box:

```sh
cravv-connect offers set mac trainer --folder ~/work/wakeword --permission tasks-auto --mode edit-in-folder
```

It asks for your password (and for `shell`, that you type `shell`). The
Mac's chat then sees the offer in `sessions("gpu-box")` and connects to
`gpu-box/new:trainer`: the GPU box starts a managed session named
`trainer-<4 characters>`, accepts the link at once at the lower of what was
asked and the offer (a request for `tasks-ask` gets `messages`, because
nobody is there to ask), and runs each message or task as a headless
`claude -p` in the folder. The answers come back over the link.

| Run mode | What the other machine's session can do | Risk |
|---|---|---|
| `read-only` (default) | Read, glob and grep inside the folder | Low: it reads what is in the folder |
| `edit-in-folder` | Also edit and write files inside the folder (not its Claude settings, git or tool configuration); no shell, no web | It can change your code in that folder |
| `shell` | Also run commands, as your user, not confined to the folder | It can do anything your user can: offer it only to a machine you trust as much as yourself |

Every run is contained (only the cravv-connect MCP server, your settings,
hooks and CLAUDE.md ignored, an allowlisted environment, its own process
group), capped (2 sessions open per offer, 30 runs an hour per link, 200 a
day per machine, 30 minutes a run, closed after 2 idle hours by default)
and audited. Details: [docs/security.md](docs/security.md#managed-sessions).

- `cravv-connect session list` shows the managed sessions running here.
- `cravv-connect session open <name>` continues a session's conversation in
  your terminal with your normal Claude settings; its queue waits until you
  exit. It warns first, because a peer drove that conversation.
- `cravv-connect session close <name>` closes it and its link.
- `cravv-connect offers list` and `offers remove <machine> <label>`
  (password) manage the offers. Claude Code keeps managed conversations
  under `~/.claude/projects/`; cravv-connect does not delete them.

## The web UI

```sh
cravv-connect ui
```

The daemon serves a page on `127.0.0.1` at a random port and opens your
browser with a one-time link. It is a thin client over the same daemon API
as the CLI, with the same password tiers; actions that need the password
ask for it in the form, and nothing stays unlocked between requests.

| Page | What you see and do |
|---|---|
| Devices | Paired machines, online and last seen; pair (with a bind code) or join, pause, resume, unpair |
| Sessions | This machine's shared sessions and their links; the sessions each device shows; connect a session (password), disconnect, restrict |
| Approvals | Link requests (accept with the password, reject) and tasks waiting for approval (password) |
| Managed | Managed sessions running here (open, close) and the offers (set or remove with the password) |
| Activity | The audit log, newest first |
| Status | The relay connection and the kill switch (on; resume with the password) |

It stops 30 minutes after the last request, or with the daemon. Use
`--no-browser` to print the link instead. See
[docs/security.md](docs/security.md#the-web-ui) for its limits.

## Cut-off controls

None of these needs a password. Agents can use the chat's own cut-offs
(`disconnect`, `restrict`, `session_close`, `kill_switch`) through MCP;
any local process can run the CLI's.

| Control | How | Effect |
|---|---|---|
| Disconnect a link | the chat's `disconnect`, the web UI's Sessions page | Closes the link on both sides. Its unfinished tasks fail (`link_closed`). Closed links never reopen |
| Restrict a link | the chat's `restrict`, the web UI's Sessions page | Lowers what the other session may do here, at once; tasks still waiting are rejected when it drops to `messages` |
| Reject | `cravv-connect link reject <link>`, the chat's `review_pending`, the web UI's Approvals page | Refuses a link request; in the chat, also a held task |
| Close a session | the chat's `session_close`; `cravv-connect session close <name>` for a managed one | Closes it and all its links; the other side learns within seconds |
| Pause | `cravv-connect pause <alias>` | Stops traffic with that machine both ways and closes every link with it. Undo with `resume-peer <alias>` (links must be requested again) |
| Unpair | `cravv-connect unpair <alias>` | Closes every link, removes the machine and its keys on both sides (best effort for the notice). Pairing again needs a new code |
| Kill switch | `cravv-connect kill`, the chat's `kill_switch`, the web UI | Stops managed runs, fails tasks claimed here (and tells their senders when it can), closes every link, stops file transfers, disconnects from the relay, and stops handling incoming messages (they wait on the relay). Until `cravv-connect resume` (password) every command and agent call is refused except `status`, `peers`, `log`, `kill`, `resume`, `reset-identity`, `offers list`, `session list`, `session close`, `ui` and `daemon stop`. Survives restarts |

## CLI reference

| Command | What it does |
|---|---|
| `setup [--relay <url>] [--relay-token <t>] [--name <n>] [--yes] [--no-agents] [--pair] [--reset]` | Guided setup: relay (or a LAN test relay), init, the daemon, agents, pairing (password). Safe to run again |
| `setup --join <join code> [--name <n>] [--yes] [--no-agents] [--reset]` | Set this machine up on the relay in a join code and pair with the machine that showed it (password) |
| `version` | Print the version |
| `init --relay <url> [--relay-token <t>] [--name <n>] [--force]` | Write `config.toml`; store the admin token for the first machine. The relay URL must be an origin, `scheme://host[:port]`, with no path. `--force` with a different relay clears this machine's relay registration so it registers again there; peers are not told, so re-pair with them |
| `daemon run [--log-file <path>]` | Run the daemon in the foreground. Logs JSON to stderr, or with `--log-file` to that file, rotated at 10 MiB with 3 old files kept |
| `daemon start` / `daemon stop` / `daemon status` | Control the daemon. `stop` asks the daemon over its socket to shut down and waits up to 10 seconds for it to exit; it never signals a process that does not answer on the socket |
| `daemon install` / `daemon uninstall` | Run the daemon at login (launchd or systemd user unit) |
| `status [--json]` | Relay connection, peers, shared sessions, queues, pending approvals, errors |
| `pair [--no-qr]` | Show a join code (with a QR code) and pair (password) |
| `join <code>` | Join with a join code for this machine's relay, or a bind code (password) |
| `peers` | List paired machines with state and machine ID |
| `alias <alias> <new-alias>` | Rename a peer locally |
| `pause <alias>` / `resume-peer <alias>` | Pause a peer (closes its links) or resume it |
| `unpair <alias> [-y]` | Remove a peer |
| `sessions <machine>` | The sessions and managed-session offers a paired machine shows this one |
| `links` | Every link on this machine: sessions, peers, permissions, state |
| `link accept <link> [--permission <level>]` | Accept a link request, at the level asked or lower (password) |
| `link reject <link>` | Reject a link request |
| `link permit <link> <messages\|tasks-ask\|tasks-auto>` | Set what the other side of a link may do here (password) |
| `approvals` | Review tasks waiting for approval interactively (password once; again after 10 minutes) |
| `approve <task-id>` / `deny <task-id>` | Decide one held task (password) |
| `offers` / `offers list [machine]` | List managed-session offers (to one machine, or to all) |
| `offers set <machine> <label> --folder <dir> --permission <messages\|tasks-auto> [--mode <read-only\|edit-in-folder\|shell>] [--max-concurrent N] [--runs-per-hour N] [--runs-per-day N] [--run-timeout D] [--idle-timeout D] [--max-turns N]` | Create or change an offer (password; `shell` asks you to type shell) |
| `offers remove <machine> <label>` | Remove an offer and close its managed sessions (password) |
| `session list` | Managed sessions running here |
| `session open <name>` | Continue a managed session's conversation in this terminal (its queue waits until you exit) |
| `session close <name>` | Close a managed session and its link |
| `ui [--no-browser]` | Open the local web UI |
| `files` | List incoming and outgoing files |
| `files accept <file-id>` | Download a file held for a human (only files held before the upgrade to v2; password) |
| `allow-path <dir>` | Allow sending files from another folder (password) |
| `kill` / `resume` | Kill switch on; off (password) |
| `reset-identity` | New machine identity; every peer must pair again (password) |
| `log [-n N]` | Recent audit log entries |
| `install [claude\|codex] [--allow-send] [--no-allow-send]` / `uninstall <agent>` | Add or remove the MCP server (and for Claude Code the hooks, the `/cravv` skill and allow rules); no argument lists agents |
| `mcp [--project-dir <dir>]` | The stdio MCP server (started by your agent) |
| `listen [--wake-file <path>]` | The background listener a shared chat runs: wait for something new, print one line, exit (wake token on stdin or in the file) |
| `hook` | Claude Code's UserPromptSubmit and Stop hook |

State lives in `~/.cravv-connect` (override with `CRAVV_HOME`): `config.toml`,
`store.db`, `audit.log`, `daemon.log` (rotated: `daemon.log.1` to `.3`),
`daemon-stderr.log` (crash output only), `daemon.sock`, `daemon.pid` (written
once the daemon owns the socket), wake files in `wake/`, managed runs'
configs in `runs/`, and received files in `files/<alias>/`. The identity key
is in the macOS Keychain, or in `store.db` on Linux.

`config.toml` keys: `relay_url`, `device_name`, `peer_quota` (bytes, default
1 GiB), and `pam_service` (macOS `chkpasswd`, the default, or `checkpw`;
Linux `login`, the default, or `system-auth`; any other value is refused).

### Relays

For real use, deploy the Cloudflare relay once: follow
[relay-cf/README.md](relay-cf/README.md). You end up with a URL such as
`https://cravv-relay.<account>.workers.dev` and an admin token.

For a local test on one machine (or a LAN), run the Go reference relay. It
keeps everything in memory and loses it on exit:

```sh
bin/cravv-relay --admin-token dev-token                          # http://127.0.0.1:8787
bin/cravv-relay --addr 0.0.0.0:8787 --origin http://192.168.1.10:8787 --admin-token dev-token   # reachable on the LAN
```

`--origin` must be exactly the URL clients use, because it is part of what
they sign (both the WebSocket login and every blob request cover the
normalized origin). The admin token can also come from
`CRAVV_RELAY_ADMIN_TOKEN`.

### Manual setup

What `setup` does, step by step:

```sh
cravv-connect init --relay https://cravv-relay.example.workers.dev --relay-token <admin token>   # first machine only; others: no token
cravv-connect daemon install      # launchd (macOS) or systemd --user (Linux); starts it now
cravv-connect install claude      # MCP server, hooks, /cravv skill, allow rules
cravv-connect pair                # on the first machine; `cravv-connect join <code>` on the other
```

The admin token is used once, to create this machine's mailbox, and then
deleted. Use `--relay-token -` to read it from stdin, and `--name` to choose
the device name peers see as a suggestion (default: the host name up to the
first `.`; lowercased to letters, digits and dashes, at most 24 characters).
A machine without a token gets its mailbox from an invite that arrives
during pairing.

`daemon install` points the service at the running binary with symlinks
resolved, so install a built binary (for example `make build`, then
`bin/cravv-connect daemon install`), not `go run`: it refuses paths under the
temporary directory or Go's build cache. It then waits up to 10 seconds for
the daemon to answer and, if it does not, says where the logs are.

### Linux password check (PAM)

Human-only actions check your login password through PAM, as the user the
daemon runs as.

- **Service:** `login` by default (Debian, Ubuntu and most distributions ship
  `/etc/pam.d/login`). On Fedora, RHEL and Arch, `system-auth` is the usual
  alternative: set `pam_service = "system-auth"` in `config.toml`. The daemon
  names a pseudo tty (`PAM_TTY=cravv-connect`) so stacks with
  `pam_securetty` work.
- **Self-test:** at start the daemon checks that a random password is
  rejected as a wrong password. If the stack accepts it, the daemon refuses
  to start. If PAM fails some other way (a missing module, a service error),
  the daemon starts, every unlock fails, and `cravv-connect status` shows
  `password check is not working: <error>`.
- **SELinux and hardened systems:** `pam_unix` checks the password of a
  non-root user through the setuid helper `unix_chkpwd`. If SELinux or a
  hardened setup keeps a user service from running it, every unlock fails;
  `status` then shows the error above, and the audit log (`ausearch -m avc`)
  shows the denial.
- **Account lockout:** a wrong password counts as a failed login for your OS
  account. With `pam_faillock` (for example RHEL's `deny=3`), the OS may lock
  the account itself before cravv-connect's own lockout (5 wrong passwords,
  15 minutes). `faillock --user $USER --reset` clears it.

## Troubleshooting

| Symptom | Fix |
|---|---|
| ``daemon not running: run `cravv-connect daemon start` `` | Start it, or `cravv-connect daemon install` so it starts at login. Logs: `~/.cravv-connect/daemon.log` (launchd, systemd and `daemon start` all run `daemon run --log-file` there), plus `~/.cravv-connect/daemon-stderr.log` (launchd, `daemon start`) or `journalctl --user -u cravv-connect` (systemd) for crashes. |
| `status` shows "relay offline" | Check the relay URL in `config.toml` and that the relay answers `GET /v1/health`. The daemon retries with backoff up to 5 minutes; messages wait in the outbox. |
| A new machine never connects | It has no mailbox yet. Either give the first machine's admin token (`setup`, `init --relay-token`) or pair (`setup --join`, `join`), which registers it with an invite. |
| The chat does not wake up when something arrives | The listener is not running: ask the chat to start it again (the next prompt also says so). In Claude Code, allow `Bash(cravv-connect listen:*)` (`cravv-connect install claude` does). A listener that prints "the wake token is not valid" belongs to a closed session: share the chat again. |
| `review_pending` shows no form | The VS Code extension declines forms: read the 4-digit code from the macOS notification and type `accept <code>` in the chat. On Linux there is no notifier: use `cravv-connect link accept <link>`, `cravv-connect approvals` or `cravv-connect ui`. After 3 wrong codes an item takes the password path only. |
| A send fails with `link_closed` | The link is not active (still pending, or closed). Check `links`; ask for a new link with `connect`. Closed links never reopen. |
| A call fails with `not_shared` | The chat has not shared a session, or its session closed or was taken over by another connection: share it again (`/cravv`). |
| `sessions <machine>` says the machine did not answer in time | It is offline or paused. Check `cravv-connect peers` and `status` on both machines. |
| `status` says a peer "runs an older cravv-connect without session links" | That machine still runs v1, and v2 refuses its link-less traffic. Upgrade it. |
| A managed session's tasks fail with `run_failed` | The daemon could not run `claude`. Set `CRAVV_CLAUDE` to its path in the daemon's service environment (a daemon started by launchd or systemd has a short `PATH`), and check `daemon.log`. `rate_limited` means a cap was hit; `folder_refused` that the offer's folder moved. |
| `status` shows `password check is not working: ...` | PAM cannot check passwords (see "Linux password check" above; on a build without cgo, rebuild with `make build`). Password-gated actions fail until it is fixed; restart the daemon afterwards. |
| `password check unavailable: ... built without PAM support` | Rebuild with cgo and the PAM headers (`make build`) and restart the daemon. |
| `pam service not allowed` | Set `pam_service` in `config.toml` to an allowed value (see above), or remove it. |
| `refusing to start: ... password verifier accepted a random password` | The daemon checks once per PAM service (and again after its `/etc/pam.d` file changes) that a random password is rejected. Your PAM service accepts anything; use one that checks your login password. |
| `keychain locked or waiting for a dialog` (macOS) | The daemon could not read its identity from the login Keychain within 10 seconds. Unlock it (`security unlock-keychain ~/Library/Keychains/login.keychain-db`) or start the daemon from your logged-in session, then start it again. |
| `too many failed password attempts; locked` | Five wrong passwords in a row lock it. Wait 15 minutes (restarting the daemon does not reset it). Every attempt is in `cravv-connect log`. |
| `pairing failed: wrong code or the exchange was interrupted` | The code is burned. Run `cravv-connect pair` again for a new one. |
| Sends to a peer say "paused" | You paused it: `cravv-connect resume-peer <alias>`. If `peers` shows "paused by peer", the other side paused you. |
| `status` warns about timestamps in the future | Fix the clock on one of the machines (messages more than 10 minutes in the future are rejected). |
| The Linux daemon stops when you log out | Run `loginctl enable-linger $USER` so systemd user services keep running. |
| Unix socket path too long (bind fails) | Keep `CRAVV_HOME` short; the OS limits socket paths to about 100 bytes. |

## Development

```sh
make vet                  # go vet ./...
make test                 # go test ./... -race -count=1 (unit, conformance against the Go relay, e2e)
make build                # the three binaries in bin/
CGO_ENABLED=0 go build ./...   # everything except PAM builds without cgo
make relay-cf-test        # relay-cf typecheck and Vitest suite (needs Node)
make conformance-cf       # the Go conformance suite against relay-cf under wrangler dev
```

Run the conformance suite against any relay:

```sh
bin/cravv-relay --addr 127.0.0.1:8787 --admin-token dev-token &
go run ./cmd/cravv-conformance --relay http://127.0.0.1:8787 --admin-token dev-token
```

`e2e/` starts an in-process relay and several real daemons in temporary
directories and drives them through the IPC API, the real MCP server, the
listener as a real process, the CLI, `setup` and the web UI. The acceptance
suite (`e2e/acceptance*_test.go`) has one test per v2 success criterion
(`TestAcceptance_<N>_...`), and `TestScenario_MacAndGPUBox` runs two
machines with two linked chats each. Tests use a fake password verifier,
keep the identity in the store instead of the Keychain, and run the test
binary itself as a stand-in for `claude` in managed sessions. Gated tests:
`CRAVV_CLAUDE_TEST=1` runs managed sessions against a real Claude Code, and
`CRAVV_BROWSER_TEST=1` drives the web UI in headless Chrome.

The docs are checked too: `TestIPCDocMatchesRegistry` compares the method
table in `protocol/ipc-v1.md` with the daemon's registry
(`CRAVV_UPDATE_DOCS=1 go test ./e2e -run TestIPCDocMatchesRegistry`
rewrites it), `TestREADMEDocumentsEveryCommand` checks that this CLI
reference names every command and flag, and `TestDocsHaveNoEmDashes`
keeps em dashes out of every Markdown file.

## License

Not yet licensed for redistribution. Open sourcing is planned.
````

- [ ] **Step 3: Run the tests**

```sh
go test ./e2e -run 'TestREADMEDocumentsEveryCommand|TestDocsHaveNoEmDashes|TestIPCDocMatchesRegistry' -count=1
go test ./scripts ./cmd/cravv-connect -run 'Readme|Attests' -count=1
```

Expected: `ok` for each package.

- [ ] **Step 4: Run the whole suite**

```sh
test -z "$(gofmt -l internal e2e cmd scripts)" && go vet ./... && go test ./... -race -count=1
```

Expected: no gofmt output, vet clean, and 36 `ok` lines (every package, `e2e` included), no `FAIL`.

- [ ] **Step 5: Commit**

```sh
git add README.md e2e/docs_test.go
git commit -F - <<'MSG'
docs: README for v2 (links and the chat hub, setup, the chat, managed sessions, web UI, cut-offs, CLI reference, troubleshooting); tests keep the CLI reference complete and every doc free of em dashes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>
MSG
```
