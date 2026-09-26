# cravv-connect v2 Phase 5: Web UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A local web page for everything the human decides or watches: paired devices (online, last seen, pair, pause, resume, unpair), shared sessions and their links (connect, disconnect, restrict), approvals (link requests and tasks-ask tasks), the audit tail, and the kill switch. `cravv-connect ui` asks the daemon to start the page on `127.0.0.1` and opens the browser with a one-time link. The page is a thin client over the existing IPC API: every browser session holds its own IPC connection into the daemon, so the IPC gates, the tiers of spec section 10 and the password lockout apply exactly as they do for the CLI.

**Architecture:**
- `internal/ipc` gains `Server.Pipe`, an in-process connection served by `ServeConn` like a socket connection (own `ConnState`, same gates, own unlock window), and the UI method names (`ui.start`, `sessions.local`, `link.connect_as`).
- `internal/api` gains `RegisterUI` with its own `UIPorts` (kept out of `Ports`, so the core port set is untouched): `ui.start` returns the launch URL, `sessions.local` lists local shared sessions for the human, and `link.connect_as` asks for a link on a named local session's behalf behind the password. All three refuse agent connections.
- `internal/webui` (new) is the UI server: `Launcher` (start on demand, launch tokens, idle stop after 30 minutes, stop with the daemon), request checks on every request (Host, `Cache-Control: no-store`, CSP and friends) and on every POST (session cookie, CSRF token, `Origin`), browser sessions (each with its own IPC connection, flashes and unlock time), a page registry (`Page`, `Action`, `Registry`, `Request.WithPassword`), `html/template` views embedded with `embed.FS`, and the five pages (Devices, Sessions, Approvals, Activity, Status). Peer text reaches a page only through `peerText`/`cleanLine` and `html/template` escaping.
- `internal/app` wires it: `app.Serve` builds a `webUI` whose browser sessions are `srv.Pipe` connections, registers the UI methods, and stops the UI (and waits for its connections) before it returns. `UIPorts` adapts the daemon.
- `internal/daemon` reports when each paired machine was last heard from (`PeerActivity.LastSeen`, `DaemonStatus.LastSeen`), carried to `ipc.PeerView.LastSeen` for the Devices page.
- `internal/cli` adds `cravv-connect ui [--no-browser]`.

**Tech Stack:** Go 1.26 standard library only for the UI (`net/http`, `html/template`, `embed`, `crypto/rand`, `crypto/subtle`; `net/http/cookiejar` in tests). No new modules, no JavaScript build step, no request leaves the machine. The existing `github.com/spf13/cobra` for the command.

**Spec:** `docs/superpowers/specs/2026-09-26-cravv-connect-v2-sessions-design.md` section 9 (web UI) with the tiers of section 10; sections 3 and 6 for context. Phase 1 plan: `docs/superpowers/plans/2026-09-26-cravv-connect-v2-phase1-links.md`.

**Verified:** every task below was implemented test-first in a scratch worktree on top of `main` at `158be83` (Phase 1 with its review fixes), one commit per task. After each commit `gofmt -l internal e2e cmd` printed nothing, `go vet ./...` was clean and `go test ./... -race -count=1` passed (e2e included). The code blocks are those commits, byte for byte.

## Global Constraints

- Everything in the Phase 1 plan's Global Constraints still holds (module path, cgo only in `internal/auth`, `crypto/rand` only, identity from the IPC connection, no em dashes in user-facing text).
- After every task: `gofmt -l internal e2e cmd` prints nothing, `go vet ./...` is clean, `go test ./... -race -count=1` passes (e2e included).
- Time: production code reads time only from `core.Clock` (launch token expiry, idle stop, unlock display). The idle check runs on a real ticker (`Options.SweepEvery`, default one minute) that only decides when to look; tests call `sweep` directly after advancing a `core.FakeClock`.
- **Thin client.** `internal/webui` holds no business rules. Every read and every change is an IPC call on the browser session's own connection (`Request.Call`); password-tier actions go through `Request.WithPassword`, which only calls `auth.unlock` with the typed password. The daemon's gates decide; the page shows their answer.
- **Merge safety (Phase 2 is written in parallel).** New code lives in new files: `internal/webui/*`, `internal/ipc/pipe.go`, `internal/ipc/methods_ui.go`, `internal/api/ui.go`, `internal/app/ui.go`, `internal/cli/cmd_ui.go` and their tests. The only edits to existing files are: `internal/ipc/client.go` (`DialContext` uses `NewClient`), `internal/ipc/methods.go` (`PeerView.LastSeen`), `internal/daemon/activity.go` (`LastSeen`), `internal/daemon/status.go` (`DaemonStatus.LastSeen`), `internal/app/app.go` (one line in the status adapter) and `internal/app/run.go` (four lines in `Serve`). `api.Ports`, `api.Register`, the MCP server and tools, hooks and the installer are not touched.
- **Request checks (every request):** `Host` is exactly `127.0.0.1:<port>` or `localhost:<port>`, otherwise 403 (DNS rebinding); every response carries `Cache-Control: no-store`, `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`.
- **State changes (every POST):** the session cookie, the browser session's CSRF token (form field `csrf`, compared in constant time) and `Origin` equal to `http://<Host>` (a missing or `null` Origin is refused); bodies are capped at 64 KiB. Pages are GET only, actions POST only.
- **Launch:** the URL is `http://127.0.0.1:<port>/launch?token=<32 random bytes, base64url>`. A token works once and for `LaunchTokenTTL` (2 minutes); at most `MaxLaunchTokens` (8) are live. It is swapped for the cookie `cravv_ui_<port>` (`HttpOnly`, `SameSite=Strict`, `Path=/`, a 32-byte random value) and a 303 to `/`, so the token leaves the address bar.
- **Browser sessions:** each has its own IPC connection (`ipc.Server.Pipe`), CSRF token, flash messages and unlock time. At most `MaxSessions` (8); the oldest ends first.
- **Lifetime:** the server listens on `127.0.0.1:0`, stops `IdleTimeout` (30 minutes) after the last request, and stops with the daemon (`Serve` waits for every UI connection before returning). A later `ui.start` starts a fresh server with a new port, new tokens and no sessions.
- **Tiers (spec 10) are the IPC gates, unchanged:**

| UI action | IPC method | Needs |
|---|---|---|
| Kill switch on | `kill` | nothing |
| Resume | `resume` | password (`GateUnlock`) |
| Pause, resume a device | `peer.pause`, `peer.resume` | nothing |
| Unpair a device | `peer.unpair` | nothing (IPC gate is `GateNone`; see Decisions) |
| Pair, join, name a device | `pair.start`, `pair.await`, `join.start`, `pair.finalize` | password |
| Ask for a link from a local session | `link.connect_as` (new) | password (`GateUnlock`) |
| Disconnect, restrict a link | `link.disconnect`, `link.restrict` | nothing |
| Accept a link request | `link.decide` accept | password (checked by the daemon) |
| Reject a link request | `link.decide` reject | nothing |
| See, approve or deny tasks-ask tasks | `approvals.list`, `approvals.decide` | password |

- **Peer text** (purposes, notes, task instructions, a device's suggested name, audit details) reaches a page only through `peerText` (unwraps a `<remote_message>` wrapper and cleans each line) or `cleanLine` (the CLI's terminal cleaning: control, format, separator and `present.IsInvisible` characters removed) and then `html/template` escaping. Nothing is ever passed as `template.HTML`.
- **Copy:** no em dashes in templates, scripts, styles or Go strings (`TestUICopyHasNoEmDashes`).
- **Honest limits (for the Phase 6 docs):** any local process of the same user can run `cravv-connect ui` and use the no-password actions, exactly as with the CLI; wrong passwords typed on the page count toward the same 15-minute lockout; the launch URL is visible briefly on the `open`/`xdg-open` command line, and a cookie for `127.0.0.1` is sent to any port on that address, so a process of another local user that wins the race for the link (single use) or receives the cookie reaches the no-password actions until the session ends.

## Decisions

- **`link.connect_as` is new.** `link.connect` is `GateShared`: it acts for the session bound to the calling connection, and a UI connection has none. Connecting from the page therefore names a local open session. Once the other side accepts, that session's chat can receive messages from it, which is the same outcome as accepting a link at `messages`; from the CLI or UI that decision needs the password, so the method is `GateUnlock`. It refuses agent connections (a registered or shared session) so an agent cannot act for a sibling session.
- **`sessions.local` is new.** No IPC method listed local shared sessions for the human (the status call only has `name (state)` strings). It returns `SharedSessionView`s (never IDs), works while killed, and refuses agent connections.
- **`ui.start` works while killed** (the page is where the human resumes) and refuses agent connections.
- **Unpair stays as the IPC gate has it (no password).** The spec 10 table lists "pair or unpair" under password only, but the Phase 1 IPC gate for `peer.unpair` is `GateNone` (a v1 cut-off, like pause and kill). The UI mirrors the IPC gate and asks for confirmation in the browser; changing the tier belongs in the IPC layer for CLI and UI together.
- **Last seen is in memory:** when the daemon last handled an envelope from the peer since it started (`-` before any traffic).
- **Not in this phase:** the QR code for join codes (join codes arrive with Phase 4 setup; the Devices page shows the bind code as text) and the managed rules page (Phase 3 IPC does not exist yet; see Seams).

## Review Focus

These failure modes follow from the spec but no happy path exercises them. Each is pinned by the named test.

1. **A launch link is replayed or outlives its purpose.** The token must work once, expire after `LaunchTokenTTL`, never be accepted when wrong, and leave the URL after the swap. Test: `TestLaunchTokenWorksOnceAndExpires` (Task 4).
2. **DNS rebinding.** A page on another name that resolves to 127.0.0.1 must get nothing, including the launch and static routes. Test: `TestHostMustBeTheUIAddress` (Task 4).
3. **Cross-site form posts.** A POST without the cookie, without or with a wrong (or another session's) CSRF token, or with a missing, foreign, other-port or `null` Origin must be refused before the daemon is called; GET on an action is 405. Test: `TestActionsNeedCookieCSRFAndOrigin` (Task 4).
4. **The password window leaks or the page bypasses the daemon.** A password typed in one browser session must unlock only that session's IPC connection; wrong passwords and the lockout must come from the daemon's Guard and show on the page; and against a real daemon a link request without the password must be refused by the IPC gate. Tests: `TestPasswordUnlocksOnlyThisBrowser` (Task 4) and `TestWebUIConnectAndAccept` (Task 7).
5. **Peer text breaks out of the page or hides itself.** A purpose or note with `</pre><script>`, an `<img onerror>`, bidi overrides and zero-width characters must render escaped and cleaned on the Sessions and Approvals pages. Test: `TestPeerStringsAreEscapedAndCleaned` (Task 6).

## File Structure

Production files (tests live next to them as `*_test.go`; each task lists its test files).

| File | Change | Responsibility |
|---|---|---|
| `internal/ipc/client.go` | Modify | `DialContext` builds its client with `NewClient`. |
| `internal/ipc/pipe.go` | Create | `NewClient`, `Server.Pipe`: in-process connections served like socket ones. |
| `internal/ipc/methods_ui.go` | Create | `ui.start`, `sessions.local`, `link.connect_as` names, params and results. |
| `internal/ipc/methods.go` | Modify | `PeerView.LastSeen`. |
| `internal/daemon/activity.go` | Modify | `PeerActivity.LastSeen`. |
| `internal/daemon/status.go` | Modify | `DaemonStatus.LastSeen`. |
| `internal/app/app.go` | Modify | The status adapter copies `LastSeen` to the peer view. |
| `internal/api/ui.go` | Create | `UIPort`, `LocalSessionPort`, `UIPorts`, `RegisterUI` and the three handlers. |
| `internal/app/ui.go` | Create | `UIPorts` adapter (`localSessions`); `webUI` (Launcher over `srv.Pipe`, waits for its connections on close). |
| `internal/app/run.go` | Modify | `Serve` builds the web UI, registers the UI methods and closes the UI before returning. |
| `internal/webui/launcher.go` | Create | Package doc, limits, `Caller`, `Dialer`, `Options`, `Launcher` (start on demand, idle sweep, stop). |
| `internal/webui/server.go` | Create | HTTP handler: headers and Host check, launch token swap, browser sessions, page and action handlers with the POST checks. |
| `internal/webui/session.go` | Create | `uiSession`: CSRF check, flashes, unlock time. |
| `internal/webui/registry.go` | Create | `Page`, `Action`, `Reply`, `Registry`, `DefaultRegistry`, `Request` (`Call`, `Form`, `Query`, `WithPassword`). |
| `internal/webui/render.go` | Create | Embedded assets, templates per page, the layout view, error messages, formatting helpers. |
| `internal/webui/text.go` | Create | `cleanLine`, `peerText`. |
| `internal/webui/links.go` | Create | Link rows for the Sessions and Approvals pages, `formLink`, `peerLines`. |
| `internal/webui/pages_status.go` | Create | Status page, kill and resume. |
| `internal/webui/pages_devices.go` | Create | Devices page, pause, resume, unpair, pairing and joining. |
| `internal/webui/pages_activity.go` | Create | Activity page (audit tail, newest first). |
| `internal/webui/pages_sessions.go` | Create | Sessions page, connect, disconnect, restrict. |
| `internal/webui/pages_approvals.go` | Create | Approvals page: link requests and tasks-ask tasks. |
| `internal/webui/assets/templates/*.html` | Create | `layout.html` (layout and password field), one file per page or step. |
| `internal/webui/assets/static/app.css`, `app.js` | Create | Styles (light and dark); the script only confirms drastic actions and auto-submits the pairing wait. |
| `internal/cli/cmd_ui.go` | Create | `cravv-connect ui [--no-browser]`. |
| `e2e/lastseen_test.go`, `e2e/webui_test.go` | Create | Last seen across two daemons; the UI end to end (connect and accept, kill and resume). |

## Seams for later phases (do not implement here)

- **Managed rules page (Phase 3).** Add `internal/webui/pages_managed.go` with `func addManaged(r *Registry)` (a `Page` plus its `Action`s calling the Phase 3 IPC methods through `rq.Call`, with `rq.WithPassword` for editing rules, which spec 10 puts behind the password), a template `internal/webui/assets/templates/managed.html` defining `content`, and one line `addManaged,` in `DefaultRegistry`. Templates are picked up by `assets/templates/*.html`; nothing else changes.
- **Join codes and QR (Phase 4).** The Devices page's "Join with a code" form posts to `/devices/join` (`join.start`); Phase 4's join codes can replace the IPC call there and add a QR image as an embedded asset or inline SVG (no external requests; `img-src 'self'` allows same-origin images).
- **Docs (Phase 6).** The honest limits above go into the security docs.

---

### Task 1: ipc: in-process pipe connections and the web UI method names

The UI must reach the daemon through the same gates as the CLI, with one connection per browser session so a password unlock belongs to that session only. `Server.Pipe` serves one end of `net.Pipe` with `ServeConn`, exactly like a socket connection minus the peer-credential check (the caller is the daemon process itself).

**Files:**
- Create: `internal/ipc/pipe.go`, `internal/ipc/methods_ui.go`
- Modify: `internal/ipc/client.go`
- Test: `internal/ipc/pipe_test.go` (new)

**Interfaces:**

Consumes: v1 `ipc.Server.ServeConn`, `ipc.Client`, `ipc.ConnState`.

Produces:

```go
// internal/ipc/pipe.go
func NewClient(conn net.Conn) *Client
func (s *Server) Pipe(ctx context.Context) (c *Client, done <-chan struct{})
// internal/ipc/methods_ui.go
const (
	MethodUIStart       = "ui.start"
	MethodSessionsLocal = "sessions.local"
	MethodLinkConnectAs = "link.connect_as"
)
type UIStartResult struct{ URL string }
type LocalSessionsResult struct{ Sessions []SharedSessionView }
type LinkConnectAsParams struct{ Session, Target, Permission, Note string }
```

- [ ] **Step 1: Write the failing tests**

Create `internal/ipc/pipe_test.go`:

```go
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func newPipeServer() *Server {
	s := NewServer(Options{})
	s.Register("unlock", func(_ context.Context, cs *ConnState, _ json.RawMessage) (any, error) {
		return UnlockResult{ExpiresAt: cs.Unlock(core.UnlockTTL)}, nil
	}, GateNone)
	s.Register("secret", func(context.Context, *ConnState, json.RawMessage) (any, error) {
		return IDResult{ID: "s3cret"}, nil
	}, GateUnlock)
	return s
}

// Each pipe is its own connection: unlocking one leaves the other locked.
func TestPipeConnectionsHaveTheirOwnState(t *testing.T) {
	s := newPipeServer()
	ctx := context.Background()
	a, _ := s.Pipe(ctx)
	defer a.Close()
	b, _ := s.Pipe(ctx)
	defer b.Close()
	if err := a.Call(ctx, "unlock", nil, nil); err != nil {
		t.Fatal(err)
	}
	var r IDResult
	if err := a.Call(ctx, "secret", nil, &r); err != nil || r.ID != "s3cret" {
		t.Fatalf("unlocked pipe: %v %+v", err, r)
	}
	if err := b.Call(ctx, "secret", nil, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("other pipe: err = %v, want ErrAuthRequired", err)
	}
}

func TestPipeEndsWhenClientCloses(t *testing.T) {
	s := newPipeServer()
	c, done := s.Pipe(context.Background())
	c.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeConn did not return after the client closed")
	}
}

func TestPipeEndsWithContext(t *testing.T) {
	s := newPipeServer()
	ctx, cancel := context.WithCancel(context.Background())
	c, done := s.Pipe(ctx)
	defer c.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ServeConn did not return after ctx ended")
	}
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client did not see the connection end")
	}
	if err := c.Call(context.Background(), "unlock", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after end: %v, want ErrClosed", err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/ipc -run '^(TestPipeConnectionsHaveTheirOwnState|TestPipeEndsWhenClientCloses|TestPipeEndsWithContext)$' -count=1
```

Expected: FAIL (fails to compile), starting with:

```
internal/ipc/pipe_test.go:28:12: s.Pipe undefined (type *Server has no field or method Pipe)
```

- [ ] **Step 3: Implement**

Create `internal/ipc/pipe.go`:

```go
package ipc

import (
	"context"
	"net"
)

// NewClient wraps an open connection to the daemon, such as one end of
// net.Pipe. The client owns conn and closes it on Close.
func NewClient(conn net.Conn) *Client {
	c := &Client{conn: conn, pending: map[uint64]chan Response{}, done: make(chan struct{})}
	go c.readLoop()
	return c
}

// Pipe opens an in-process connection to s. The server end is served by
// ServeConn exactly like a socket connection: it has its own ConnState, so
// the same gates apply and its password unlock window is its own. Only the
// peer-credential check is skipped, because the caller is this process.
// done is closed once ServeConn has returned, after the client closed the
// connection or ctx ended.
func (s *Server) Pipe(ctx context.Context) (c *Client, done <-chan struct{}) {
	serverEnd, clientEnd := net.Pipe()
	d := make(chan struct{})
	go func() {
		defer close(d)
		s.ServeConn(ctx, serverEnd)
	}()
	return NewClient(clientEnd), d
}
```

Create `internal/ipc/methods_ui.go`:

```go
package ipc

// Methods of the local web UI (v2 phase 5). They are registered by
// api.RegisterUI; the gates are set there.
const (
	// MethodUIStart starts the local web UI if it is not running and returns
	// a URL carrying a new one-time launch token.
	MethodUIStart = "ui.start"
	// MethodSessionsLocal lists this machine's open and away shared sessions
	// for the human (CLI or UI connections only).
	MethodSessionsLocal = "sessions.local"
	// MethodLinkConnectAs asks for a link from a named local session on the
	// human's behalf (CLI or UI connections only; needs the password).
	MethodLinkConnectAs = "link.connect_as"
)

// UIStartResult is the launch URL. The token in it works once.
type UIStartResult struct {
	URL string `json:"url"`
}

// LocalSessionsResult lists local shared sessions. Views never carry IDs.
type LocalSessionsResult struct {
	Sessions []SharedSessionView `json:"sessions"`
}

// LinkConnectAsParams asks target ("machine/session") for a link from the
// local open session named Session. Permission is what that session proposes
// to do on the other side.
type LinkConnectAsParams struct {
	Session    string `json:"session"`
	Target     string `json:"target"`
	Permission string `json:"permission"`
	Note       string `json:"note,omitempty"`
}
```

In `internal/ipc/client.go`, `DialContext`, replace the last three lines of the function:

```go
	c := &Client{conn: conn, pending: map[uint64]chan Response{}, done: make(chan struct{})}
	go c.readLoop()
	return c, nil
```

with:

```go
	return NewClient(conn), nil
```

- [ ] **Step 4: Run the tests to see them pass**

```bash
go test ./internal/ipc -run '^(TestPipeConnectionsHaveTheirOwnState|TestPipeEndsWhenClientCloses|TestPipeEndsWithContext)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/ipc/client.go internal/ipc/methods_ui.go internal/ipc/pipe.go internal/ipc/pipe_test.go
git commit -m "ipc: in-process pipe connections and the web UI method names

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: status: when each paired machine was last heard from

The Devices page shows "last seen". The daemon already records when each peer last sent a handled envelope (`PeerActivity`, used for `Online`); this task exposes it through the status snapshot to `ipc.PeerView` (`machines`, `peer.list`, `status`). It is in memory: zero until the peer is heard from after a daemon start.

**Files:**
- Modify: `internal/daemon/activity.go`, `internal/daemon/status.go`, `internal/ipc/methods.go`, `internal/app/app.go`
- Test: `internal/daemon/lastseen_test.go` (new), `e2e/lastseen_test.go` (new)

**Interfaces:**

Consumes: `daemon.PeerActivity`, `daemon.StatusService`, the Phase 1 test fixtures `d2Tasks`, `d2Peer`, `mailboxSlot`, `newFakeMailbox` and the e2e helpers `NewPair`, `Share`, `Connect`, `PeerView`, `Eventually`.

Produces:

```go
// internal/daemon/activity.go
func (a *PeerActivity) LastSeen(id core.MachineID) (time.Time, bool)
// internal/daemon/status.go
type DaemonStatus struct { ...; LastSeen map[core.MachineID]time.Time }
// internal/ipc/methods.go
type PeerView struct { ...; LastSeen time.Time `json:"last_seen,omitzero"` }
```

**Design notes:**
- The new `DaemonStatus` field goes last and the `PeerView` field gets its comment on its own lines, so `gofmt` does not realign the existing lines (smaller diff for the parallel Phase 2 merge).

- [ ] **Step 1: Write the failing tests**

Create `internal/daemon/lastseen_test.go`:

```go
package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

func TestPeerActivityLastSeen(t *testing.T) {
	ctx := context.Background()
	clock := core.NewFakeClock(d2Epoch)
	act := NewPeerActivity(clock)
	if _, ok := act.LastSeen("m1"); ok {
		t.Fatal("a peer never heard from has a last seen time")
	}
	h := act.Wrap(HandlerFunc(func(context.Context, store.Peer, core.Envelope) error { return nil }))
	if err := h.Handle(ctx, store.Peer{MachineID: "m1"}, core.Envelope{}); err != nil {
		t.Fatal(err)
	}
	heard := clock.Now()
	clock.Advance(OnlineWindow + time.Minute)
	got, ok := act.LastSeen("m1")
	if !ok || !got.Equal(heard) {
		t.Fatalf("LastSeen = %v %v, want %v", got, ok, heard)
	}
	if act.Online("m1") {
		t.Fatal("still online after the window")
	}
}

// Status reports when each peer was last heard from, also after it went
// offline, and nothing for a peer never heard from.
func TestStatusReportsLastSeen(t *testing.T) {
	ctx := context.Background()
	e := d2Tasks(t, core.PermTasksAsk)
	quiet, _ := d2Peer(t, e.st, "old-mac")
	act := NewPeerActivity(e.clock)
	h := act.Wrap(HandlerFunc(func(context.Context, store.Peer, core.Envelope) error { return nil }))
	if err := h.Handle(ctx, e.peer, core.Envelope{}); err != nil {
		t.Fatal(err)
	}
	heard := e.clock.Now()
	e.clock.Advance(OnlineWindow + time.Second)
	slot := &mailboxSlot{}
	slot.set(newFakeMailbox(&callLog{}))
	svc := NewStatusService(StatusDeps{
		MachineID: "me", Mailboxes: slot, Killed: func() bool { return false },
		Peers: e.st, Outbox: e.st, Shared: e.shared, Inbox: e.inbox, Tasks: e.tasks, Activity: act,
	})
	got, err := svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSeen[e.peer.MachineID].Equal(heard) || got.Online[e.peer.MachineID] {
		t.Fatalf("peer: last seen %v online %v, want %v and offline", got.LastSeen[e.peer.MachineID], got.Online[e.peer.MachineID], heard)
	}
	if at, ok := got.LastSeen[quiet.MachineID]; ok {
		t.Fatalf("quiet peer last seen %v, want none", at)
	}
}
```

Create `e2e/lastseen_test.go`:

```go
package e2e

import (
	"testing"
	"time"
)

// A paired machine's view carries when it was last heard from.
func TestMachinesShowLastSeen(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	if v, _ := b.PeerView("alice"); !v.LastSeen.IsZero() {
		t.Fatalf("last seen %v before any traffic", v.LastSeen)
	}
	lead := a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	Connect(t, lead, "bob/trainer", "messages", "")
	Eventually(t, 10*time.Second, "bob hears from alice", func() bool {
		v, _ := b.PeerView("alice")
		return !v.LastSeen.IsZero() && time.Since(v.LastSeen) < time.Minute
	})
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/daemon -run '^(TestPeerActivityLastSeen|TestStatusReportsLastSeen)$' -count=1
go test ./e2e -run '^TestMachinesShowLastSeen$' -count=1
```

Expected: both FAIL (fail to compile), starting with:

```
internal/daemon/lastseen_test.go:16:18: act.LastSeen undefined (type *PeerActivity has no field or method LastSeen)
e2e/lastseen_test.go:12:37: v.LastSeen undefined (type ipc.PeerView has no field or method LastSeen)
```

- [ ] **Step 3: Implement**

Append to `internal/daemon/activity.go`:

```go

// LastSeen returns when the peer last sent us a handled envelope since the
// daemon started, and false if it has not.
func (a *PeerActivity) LastSeen(id core.MachineID) (time.Time, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.seen[id]
	return t, ok
}
```

Modify `internal/daemon/status.go`:

1. Add `"time"` to the imports:

```go
import (
	"context"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)
```

2. In `type DaemonStatus`, after the `Errors` field, add:

```go
	LastSeen         map[core.MachineID]time.Time // when each peer was last heard from since the daemon started
```

3. In `Status`, replace the `st := DaemonStatus{...}` literal with:

```go
	st := DaemonStatus{
		MachineID: s.d.MachineID, DeviceName: s.d.DeviceName, RelayURL: s.d.RelayURL,
		RelayConnected: connected, Killed: s.d.Killed(), Online: map[core.MachineID]bool{},
		LastSeen: map[core.MachineID]time.Time{},
	}
```

4. In the `for _, p := range peers` loop, after the `st.Online[...] = ...` line, add:

```go
		if at, ok := s.d.Activity.LastSeen(p.MachineID); ok {
			st.LastSeen[p.MachineID] = at
		}
```

In `internal/ipc/methods.go`, `type PeerView`, after the `PairedAt` field, add:

```go
	// LastSeen is when the peer was last heard from since the daemon
	// started; zero (omitted) if it has not been.
	LastSeen time.Time `json:"last_seen,omitzero"`
```

In `internal/app/app.go`, `func (a status) Status`, after `v.Online = s.Online[p.MachineID]`, add:

```go
		v.LastSeen = s.LastSeen[p.MachineID]
```

- [ ] **Step 4: Run the tests to see them pass**

```bash
go test ./internal/daemon -run '^(TestPeerActivityLastSeen|TestStatusReportsLastSeen)$' -count=1 -race
go test ./e2e -run '^TestMachinesShowLastSeen$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add e2e/lastseen_test.go internal/app/app.go internal/daemon/activity.go internal/daemon/lastseen_test.go internal/daemon/status.go internal/ipc/methods.go
git commit -m "status: when each paired machine was last heard from

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: api: ui.start, sessions.local and link.connect_as for the web UI

The three methods the UI needs beyond Phase 1's IPC API, registered by their own `RegisterUI` with their own `UIPorts`, so `api.Ports` and `api.Register` stay as they are. `app.UIPorts` adapts the daemon; the `ui` port is filled in Task 7.

**Files:**
- Create: `internal/api/ui.go`, `internal/app/ui.go`
- Test: `internal/api/ui_test.go` (new), `internal/app/ui_test.go` (new)

**Interfaces:**

Consumes: `api.LinkPort` (Phase 1), `api.NewServer`, `ipc.Server.Pipe` (Task 1), the api test fakes `newWorld`, `fLinks`, `bg`; `daemon.SessionService.List`, the app `shared.view` and `links` adapters.

Produces:

```go
// internal/api/ui.go
type UIPort interface {
	Start(ctx context.Context) (string, error)
}
type LocalSessionPort interface {
	Local(ctx context.Context) ([]ipc.SharedSessionView, error)
	OpenByName(ctx context.Context, name string) (string, error)
}
type UIPorts struct {
	UI    UIPort
	Local LocalSessionPort
	Links LinkPort
}
func RegisterUI(s *ipc.Server, p UIPorts)
// internal/app/ui.go
func UIPorts(d *daemon.Daemon, ui api.UIPort) api.UIPorts
```

**Design notes:**
- Gates: `ui.start` and `sessions.local` are `GateAllowWhenKilled` (the page must work while killed); `link.connect_as` is `GateUnlock` (see Decisions). All three refuse a connection that registered or shared a session with `ipc.ErrBadRequest`.
- `link.connect_as` resolves the name among open sessions only (an away session cannot ask for a link, as `LinkService.Connect` requires open) and then calls the same `LinkPort.Connect` as `link.connect`.

- [ ] **Step 1: Write the failing tests**

Create `internal/api/ui_test.go`:

```go
package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

type fUI struct{ calls int }

func (f *fUI) Start(context.Context) (string, error) {
	f.calls++
	return fmt.Sprintf("http://127.0.0.1:4000/launch?token=t%d", f.calls), nil
}

type fLocal struct{}

func (fLocal) Local(context.Context) ([]ipc.SharedSessionView, error) {
	return []ipc.SharedSessionView{{Name: "lead", State: "open", Visibility: "all-peers"}, {Name: "nap", State: "away"}}, nil
}

func (fLocal) OpenByName(_ context.Context, name string) (string, error) {
	if name == "lead" {
		return "S-lead", nil
	}
	return "", core.ErrNotFound
}

// uiServer serves the real method groups plus RegisterUI over in-process
// pipes.
func uiServer(t *testing.T) (*world, *fUI, func() *ipc.Client) {
	t.Helper()
	w := newWorld()
	ui := &fUI{}
	srv := NewServer(w.ports(), core.NewFakeClock(time.Unix(1_700_000_000, 0)), nil)
	RegisterUI(srv, UIPorts{UI: ui, Local: fLocal{}, Links: fLinks{w.lw}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return w, ui, func() *ipc.Client {
		c, _ := srv.Pipe(ctx)
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func TestUIStartReturnsLaunchURL(t *testing.T) {
	_, ui, dial := uiServer(t)
	var r ipc.UIStartResult
	if err := dial().Call(bg, ipc.MethodUIStart, nil, &r); err != nil {
		t.Fatal(err)
	}
	if r.URL != "http://127.0.0.1:4000/launch?token=t1" {
		t.Fatalf("url %q", r.URL)
	}
	// It works while killed, so the human can resume from the page.
	if err := dial().Call(bg, ipc.MethodKill, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := dial().Call(bg, ipc.MethodUIStart, nil, &r); err != nil || ui.calls != 2 {
		t.Fatalf("while killed: %v (calls %d)", err, ui.calls)
	}
}

// Agent connections cannot start the UI, list every local session or ask
// for links on another session's behalf.
func TestUIMethodsRefuseAgentConnections(t *testing.T) {
	_, ui, dial := uiServer(t)
	c := dial()
	if err := c.Call(bg, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: "/work/proj"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{ipc.MethodUIStart, ipc.MethodSessionsLocal} {
		if err := c.Call(bg, m, nil, nil); !errors.Is(err, ipc.ErrBadRequest) {
			t.Errorf("%s from an agent: %v", m, err)
		}
	}
	err := c.Call(bg, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "lead", Target: "gpu-box/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, ipc.ErrBadRequest) {
		t.Errorf("link.connect_as from an agent: %v", err)
	}
	if ui.calls != 0 {
		t.Fatalf("UI started %d times", ui.calls)
	}
}

func TestSessionsLocalListsViews(t *testing.T) {
	_, _, dial := uiServer(t)
	var r ipc.LocalSessionsResult
	if err := dial().Call(bg, ipc.MethodSessionsLocal, nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 2 || r.Sessions[0].Name != "lead" || r.Sessions[1].State != "away" {
		t.Fatalf("sessions %+v", r.Sessions)
	}
}

// link.connect_as needs the password and an open local session by name,
// then connects exactly like the session's own link.connect.
func TestLinkConnectAsNeedsPasswordAndOpenSession(t *testing.T) {
	w, _, dial := uiServer(t)
	c := dial()
	p := ipc.LinkConnectAsParams{Session: "lead", Target: "gpu-box/trainer", Permission: "tasks-ask", Note: "hi"}
	if err := c.Call(bg, ipc.MethodLinkConnectAs, p, nil); !errors.Is(err, core.ErrAuthRequired) {
		t.Fatalf("without password: %v", err)
	}
	if err := c.Call(bg, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "hunter2"}, nil); err != nil {
		t.Fatal(err)
	}
	var v ipc.LinkView
	if err := c.Call(bg, ipc.MethodLinkConnectAs, p, &v); err != nil {
		t.Fatal(err)
	}
	if got := w.lw.last(); got != "connect S-lead gpu-box/trainer tasks-ask hi" || v.State != "pending" {
		t.Fatalf("connect call %q, view %+v", got, v)
	}
	if err := c.Call(bg, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "nap", Target: "gpu-box/trainer", Permission: "messages"}, nil); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("away session: %v", err)
	}
	if err := c.Call(bg, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "lead", Permission: "messages"}, nil); !errors.Is(err, ipc.ErrBadRequest) {
		t.Fatalf("missing target: %v", err)
	}
}
```

Create `internal/app/ui_test.go`:

```go
package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

// uiDaemon serves a real daemon with the UI methods over in-process pipes.
func uiDaemon(t *testing.T) (dir string, dial func() *ipc.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "appui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := config.Paths{
		Home: dir, Config: filepath.Join(dir, "config.toml"), DB: filepath.Join(dir, "store.db"),
		Audit: filepath.Join(dir, "audit.log"), Socket: filepath.Join(dir, "d.sock"),
		Files: filepath.Join(dir, "files"), Log: filepath.Join(dir, "daemon.log"),
	}
	d, err := daemon.New(daemon.Options{
		Paths: paths, Config: config.Config{DeviceName: "test-mac"}, Verifier: auth.Fake{Password: "pw"}, Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore { return daemon.SettingsIdentityStore{Settings: s} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	srv := api.NewServer(Ports(d), core.SystemClock{}, nil)
	api.RegisterUI(srv, UIPorts(d, nil))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return dir, func() *ipc.Client {
		c, _ := srv.Pipe(ctx)
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func TestUIPortsAgainstRealDaemon(t *testing.T) {
	dir, dial := uiDaemon(t)
	ctx := context.Background()
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(proj, 0o700)
	share := func(name, vis string) *ipc.Client {
		c := dial()
		if err := c.Call(ctx, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: proj}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Call(ctx, ipc.MethodSessionShare, ipc.SessionShareParams{Name: name, Purpose: name + " work", Visibility: vis}, nil); err != nil {
			t.Fatal(err)
		}
		return c
	}
	share("lead", "all-peers")
	share("gone", "private").Call(ctx, ipc.MethodSessionClose, nil, nil)

	human := dial()
	var r ipc.LocalSessionsResult
	if err := human.Call(ctx, ipc.MethodSessionsLocal, nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 1 || r.Sessions[0].Name != "lead" || r.Sessions[0].Visibility != "all-peers" || r.Sessions[0].Purpose != "lead work" {
		t.Fatalf("local sessions %+v", r.Sessions)
	}
	if err := human.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "pw"}, nil); err != nil {
		t.Fatal(err)
	}
	err := human.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "gone", Target: "bob/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), `no open session "gone"`) {
		t.Fatalf("closed session: %v", err)
	}
	// An open session gets as far as the target machine, which is not paired.
	err = human.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "lead", Target: "bob/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), `peer "bob"`) {
		t.Fatalf("open session: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/api -run '^(TestUIStartReturnsLaunchURL|TestUIMethodsRefuseAgentConnections|TestSessionsLocalListsViews|TestLinkConnectAsNeedsPasswordAndOpenSession)$' -count=1
go test ./internal/app -run '^TestUIPortsAgainstRealDaemon$' -count=1
```

Expected: both FAIL (fail to compile), starting with:

```
internal/api/ui_test.go:41:2: undefined: RegisterUI
internal/app/ui_test.go:42:6: undefined: api.RegisterUI
```

- [ ] **Step 3: Implement**

Create `internal/api/ui.go`:

```go
package api

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// UIPort starts the local web UI and returns a URL with a one-time launch
// token.
type UIPort interface {
	Start(ctx context.Context) (string, error)
}

// LocalSessionPort lists this machine's shared sessions for the human and
// finds an open one by name. Views never carry session IDs.
type LocalSessionPort interface {
	Local(ctx context.Context) ([]ipc.SharedSessionView, error)
	// OpenByName returns the ID of the open session called name, or
	// core.ErrNotFound.
	OpenByName(ctx context.Context, name string) (string, error)
}

// UIPorts are what the web UI methods need. They are kept apart from Ports
// so the UI group registers on its own (RegisterUI) and adds no field to
// the core port set.
type UIPorts struct {
	UI    UIPort
	Local LocalSessionPort
	Links LinkPort
}

// errHumanOnly refuses an agent connection (one that registered a session)
// a method meant for the owner's CLI or web UI.
var errHumanOnly = fmt.Errorf("%w: only the owner's CLI or web UI may call this, not an agent connection", ipc.ErrBadRequest)

type uiHandlers struct{ p UIPorts }

// RegisterUI adds the web UI methods to s.
func RegisterUI(s *ipc.Server, p UIPorts) {
	u := uiHandlers{p: p}
	// The UI can do nothing a local process could not do over IPC, and it
	// must work while killed so the human can resume from it.
	s.Register(ipc.MethodUIStart, ipc.Typed(u.start), ipc.GateAllowWhenKilled)
	s.Register(ipc.MethodSessionsLocal, ipc.Typed(u.local), ipc.GateAllowWhenKilled)
	// Asking for a link on a session's behalf lets the other side message
	// that chat once it accepts, like accepting a link at messages: from
	// the CLI or UI that decision needs the password.
	s.Register(ipc.MethodLinkConnectAs, ipc.Typed(u.connectAs), ipc.GateUnlock)
}

func humanOnly(cs *ipc.ConnState) error {
	if cs.Session() != "" || cs.Shared() != "" {
		return errHumanOnly
	}
	return nil
}

func (u uiHandlers) start(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	if u.p.UI == nil {
		return nil, fmt.Errorf("%w: this daemon has no web UI", ipc.ErrBadRequest)
	}
	url, err := u.p.UI.Start(ctx)
	if err != nil {
		return nil, err
	}
	return ipc.UIStartResult{URL: url}, nil
}

func (u uiHandlers) local(ctx context.Context, cs *ipc.ConnState, _ ipc.Empty) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	list, err := u.p.Local.Local(ctx)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []ipc.SharedSessionView{}
	}
	return ipc.LocalSessionsResult{Sessions: list}, nil
}

func (u uiHandlers) connectAs(ctx context.Context, cs *ipc.ConnState, p ipc.LinkConnectAsParams) (any, error) {
	if err := humanOnly(cs); err != nil {
		return nil, err
	}
	for _, f := range []struct{ name, value string }{{"session", p.Session}, {"target", p.Target}, {"permission", p.Permission}} {
		if err := required(f.name, f.value); err != nil {
			return nil, err
		}
	}
	id, err := u.p.Local.OpenByName(ctx, p.Session)
	if err != nil {
		return nil, err
	}
	return u.p.Links.Connect(ctx, id, p.Target, p.Permission, p.Note)
}
```

Create `internal/app/ui.go`:

```go
package app

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// UIPorts adapts the daemon to the web UI methods. ui starts the web server
// (Serve passes the webui.Launcher); link requests reuse the links adapter.
func UIPorts(d *daemon.Daemon, ui api.UIPort) api.UIPorts {
	return api.UIPorts{UI: ui, Local: localSessions{d}, Links: links{d}}
}

// localSessions adapts the daemon's SessionService to api.LocalSessionPort.
type localSessions struct{ d *daemon.Daemon }

func (a localSessions) Local(ctx context.Context) ([]ipc.SharedSessionView, error) {
	list, err := a.d.Shared().List(ctx, core.SessionOpen, core.SessionAway)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.SharedSessionView, 0, len(list))
	for _, s := range list {
		out = append(out, shared{a.d}.view(ctx, s))
	}
	return out, nil
}

func (a localSessions) OpenByName(ctx context.Context, name string) (string, error) {
	list, err := a.d.Shared().List(ctx, core.SessionOpen)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("no open session %q on this machine: %w", name, core.ErrNotFound)
}
```

- [ ] **Step 4: Run the tests to see them pass**

```bash
go test ./internal/api -run '^(TestUIStartReturnsLaunchURL|TestUIMethodsRefuseAgentConnections|TestSessionsLocalListsViews|TestLinkConnectAsNeedsPasswordAndOpenSession)$' -count=1 -race
go test ./internal/app -run '^TestUIPortsAgainstRealDaemon$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/api/ui.go internal/api/ui_test.go internal/app/ui.go internal/app/ui_test.go
git commit -m "api: ui.start, sessions.local and link.connect_as for the web UI

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: webui: launcher, request checks, browser sessions and the Status page

The UI server and everything security-relevant about it, with the first page (Status and the kill switch) so the checks are tested on a real page and action. The tests drive a real `Launcher` over loopback HTTP against a scripted `ipc.Server` reached through `Server.Pipe`, so the password gate and the kill switch gate are the real IPC gates.

**Files:**
- Create: `internal/webui/launcher.go`, `internal/webui/server.go`, `internal/webui/session.go`, `internal/webui/registry.go`, `internal/webui/render.go`, `internal/webui/text.go`, `internal/webui/pages_status.go`, `internal/webui/assets/templates/layout.html`, `internal/webui/assets/templates/status.html`, `internal/webui/assets/static/app.css`, `internal/webui/assets/static/app.js`
- Test: `internal/webui/harness_test.go` (new), `internal/webui/server_test.go` (new), `internal/webui/text_test.go` (new)

**Interfaces:**

Consumes: `core.Clock`, `ipc` method names and views, `ipc.Server.Pipe` (tests), `present.IsInvisible`, `present.Wrap` (tests).

Produces:

```go
// internal/webui/launcher.go
const (
	IdleTimeout       = 30 * time.Minute
	LaunchTokenTTL    = 2 * time.Minute
	MaxSessions       = 8
	MaxLaunchTokens   = 8
	DefaultSweepEvery = time.Minute
)
var ErrStopping = errors.New("the daemon is stopping")
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
	Close() error
}
type Dialer func() (Caller, error)
type Options struct {
	Clock      core.Clock
	Dial       Dialer
	Pages      *Registry
	Logger     *slog.Logger
	SweepEvery time.Duration
	Listen     func() (net.Listener, error)
}
type Launcher struct{ ... }
func NewLauncher(ctx context.Context, o Options) *Launcher
func (l *Launcher) Start(context.Context) (string, error) // implements api.UIPort
func (l *Launcher) Running() bool
func (l *Launcher) Stop()
// internal/webui/registry.go
type Page struct {
	Path, Title, Template string
	Load func(ctx context.Context, rq *Request) (any, error)
}
type Action struct {
	Path, Back string
	Run  func(ctx context.Context, rq *Request) (Reply, error)
}
type Reply struct {
	To, Notice, Template, Title string
	Data                        any
}
type Registry struct{ ... }
func (r *Registry) AddPage(p Page)
func (r *Registry) AddAction(a Action)
func (r *Registry) Pages() []Page
func (r *Registry) Actions() []Action
func DefaultRegistry() *Registry
type Request struct {
	HTTP *http.Request
	// unexported: the browser session
}
func (rq *Request) Call(ctx context.Context, method string, params, result any) error
func (rq *Request) Form(name string) string
func (rq *Request) Query(name string) string
func (rq *Request) WithPassword(ctx context.Context, fn func() error) error
```

**Design notes:**
- One `server` per running instance: an idle stop throws away its port, tokens and sessions, and closes every session's IPC connection.
- `ServeHTTP` sets the headers first, so even the 403 for a bad Host and the plain-text 401/403 errors are `no-store`.
- An action that fails redirects back with the daemon's message as a flash (`message` rewords only the password, lockout, kill switch and closed-connection errors); an action may instead render a template in place (`Reply.Template`) for multi-step flows (pairing, Task 5).
- The password field is `required` only while this browser session is locked (`(not $.Unlocked)` in the templates); `WithPassword` unlocks only when a password was typed and otherwise lets the daemon's `auth_required` through.
- `peerText` undoes `present.Wrap`'s body escaping (`&lt;`, `&gt;`, `&amp;`) before `html/template` escapes again, so the page shows exactly what the peer wrote (a literal `&lt;` stays `&lt;`), minus hiding characters.
- The script is optional: every action is a plain form. It only confirms drastic actions (`data-confirm`) and auto-submits the pairing wait (`data-autosubmit`).

- [ ] **Step 1: Write the failing tests**

Create `internal/webui/harness_test.go`:

```go
package webui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// fakeDaemon is a real ipc.Server with scripted handlers. UI sessions reach
// it over in-process pipes, so its gates (password, kill switch) are real.
// auth.unlock accepts "pw"; five wrong passwords lock it.
type fakeDaemon struct {
	t      *testing.T
	clock  *core.FakeClock
	srv    *ipc.Server
	mu     sync.Mutex
	calls  []string
	killed bool
	fails  int
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	fd := &fakeDaemon{t: t, clock: core.NewFakeClock(time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))}
	fd.srv = ipc.NewServer(ipc.Options{Clock: fd.clock, Killed: fd.isKilled})
	fd.handle(ipc.MethodAuthUnlock, ipc.GateAllowWhenKilled, func(cs *ipc.ConnState, p json.RawMessage) (any, error) {
		var up ipc.UnlockParams
		json.Unmarshal(p, &up)
		fd.mu.Lock()
		defer fd.mu.Unlock()
		if fd.fails >= core.LockoutFailures {
			return nil, core.ErrLocked
		}
		if up.Password != "pw" {
			fd.fails++
			return nil, core.ErrBadPassword
		}
		return ipc.UnlockResult{ExpiresAt: cs.Unlock(core.UnlockTTL)}, nil
	})
	return fd
}

// handle registers method; every call is recorded as "method params".
func (fd *fakeDaemon) handle(method string, gate ipc.Gate, fn func(cs *ipc.ConnState, p json.RawMessage) (any, error)) {
	fd.srv.Register(method, func(_ context.Context, cs *ipc.ConnState, p json.RawMessage) (any, error) {
		fd.mu.Lock()
		fd.calls = append(fd.calls, method+" "+string(p))
		fd.mu.Unlock()
		return fn(cs, p)
	}, gate)
}

func (fd *fakeDaemon) reply(method string, gate ipc.Gate, result any) {
	fd.handle(method, gate, func(*ipc.ConnState, json.RawMessage) (any, error) { return result, nil })
}

func (fd *fakeDaemon) isKilled() bool {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	return fd.killed
}

func (fd *fakeDaemon) setKilled(k bool) {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	fd.killed = k
}

// called returns the recorded calls of method, params only.
func (fd *fakeDaemon) called(method string) []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	var out []string
	for _, c := range fd.calls {
		if m, p, _ := strings.Cut(c, " "); m == method {
			out = append(out, p)
		}
	}
	return out
}

// ui is a Launcher over a fakeDaemon, on the daemon's fake clock.
type ui struct {
	t      *testing.T
	clock  *core.FakeClock
	fd     *fakeDaemon
	l      *Launcher
	cancel context.CancelFunc
}

func newUI(t *testing.T, fd *fakeDaemon) *ui {
	t.Helper()
	clock := fd.clock
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dial := func() (Caller, error) {
		c, _ := fd.srv.Pipe(ctx)
		return c, nil
	}
	return &ui{t: t, clock: clock, fd: fd, cancel: cancel, l: NewLauncher(ctx, Options{Clock: clock, Dial: dial})}
}

// launchURL asks for a new launch URL.
func (u *ui) launchURL() string {
	u.t.Helper()
	s, err := u.l.Start(context.Background())
	if err != nil {
		u.t.Fatal(err)
	}
	return s
}

// browser is an HTTP client that keeps one cookie and never follows
// redirects.
type browser struct {
	t      *testing.T
	base   string
	cookie *http.Cookie
	client *http.Client
}

type resp struct {
	code   int
	header http.Header
	body   string
}

func newBrowser(t *testing.T, launch string) *browser {
	u, err := url.Parse(launch)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{t: t, base: "http://" + u.Host, client: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// open launches a browser session and returns it with the cookie set.
func (u *ui) open() *browser {
	u.t.Helper()
	launch := u.launchURL()
	b := newBrowser(u.t, launch)
	r := b.do("GET", launch, nil, nil)
	if r.code != http.StatusSeeOther {
		u.t.Fatalf("launch: %d %s", r.code, r.body)
	}
	return b
}

// do sends a request. A relative target is on the UI's origin. It sends
// the cookie when set and records a new one.
func (b *browser) do(method, target string, form url.Values, edit func(*http.Request)) resp {
	b.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = b.base + target
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if b.cookie != nil {
		req.AddCookie(b.cookie)
	}
	if edit != nil {
		edit(req)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	for _, c := range res.Cookies() {
		b.cookie = c
	}
	return resp{code: res.StatusCode, header: res.Header, body: string(raw)}
}

func (b *browser) get(path string) resp { b.t.Helper(); return b.do("GET", path, nil, nil) }

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// csrf reads the form token from a page.
func (b *browser) csrf(page string) string {
	b.t.Helper()
	r := b.get(page)
	m := csrfField.FindStringSubmatch(r.body)
	if m == nil {
		b.t.Fatalf("no csrf field on %s (%d): %s", page, r.code, r.body)
	}
	return m[1]
}

// post submits a form from page like a browser would: with the page's CSRF
// token and this origin.
func (b *browser) post(page, action string, form url.Values) resp {
	b.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", b.csrf(page))
	return b.do("POST", action, form, func(r *http.Request) { r.Header.Set("Origin", b.base) })
}

// follow posts and then loads the page the action redirected to.
func (b *browser) follow(page, action string, form url.Values) resp {
	b.t.Helper()
	r := b.post(page, action, form)
	if r.code != http.StatusSeeOther {
		b.t.Fatalf("%s: %d %s", action, r.code, r.body)
	}
	return b.get(r.header.Get("Location"))
}

func wantContains(t *testing.T, body string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(body, p) {
			t.Errorf("page lacks %q:\n%s", p, body)
		}
	}
}
```

Create `internal/webui/server_test.go`:

```go
package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
)

func statusDaemon(t *testing.T) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.handle(ipc.MethodStatus, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.StatusResult{MachineID: "M123", DeviceName: "mac", RelayURL: "https://relay.test", RelayConnected: true, Killed: fd.isKilled()}, nil
	})
	fd.handle(ipc.MethodKill, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		fd.setKilled(true)
		return nil, nil
	})
	fd.handle(ipc.MethodResume, ipc.GateUnlock|ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		fd.setKilled(false)
		return nil, nil
	})
	return fd
}

// The launch token is swapped for an HttpOnly, SameSite=Strict cookie and a
// redirect to a URL without the token.
func TestLaunchSwapsTokenForCookie(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	launch := u.launchURL()
	if !strings.HasPrefix(launch, "http://127.0.0.1:") || !strings.Contains(launch, "/launch?token=") {
		t.Fatalf("launch URL %q", launch)
	}
	b := newBrowser(t, launch)
	r := b.do("GET", launch, nil, nil)
	if r.code != http.StatusSeeOther || r.header.Get("Location") != "/" {
		t.Fatalf("launch: %d to %q", r.code, r.header.Get("Location"))
	}
	c := b.cookie
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || len(c.Value) < 40 {
		t.Fatalf("cookie %+v", c)
	}
	if strings.Contains(r.header.Get("Set-Cookie"), "token=") {
		t.Fatal("cookie carries the token")
	}
	home := b.get("/")
	if first := DefaultRegistry().Pages()[0].Path; home.code != http.StatusSeeOther || home.header.Get("Location") != first {
		t.Fatalf("home: %d to %q", home.code, home.header.Get("Location"))
	}
	wantContains(t, b.get("/status").body, "M123", "https://relay.test (connected)")
}

// A launch token works once, and not after LaunchTokenTTL.
func TestLaunchTokenWorksOnceAndExpires(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	launch := u.launchURL()
	newBrowser(t, launch).do("GET", launch, nil, nil)
	again := newBrowser(t, launch)
	if r := again.do("GET", launch, nil, nil); r.code != http.StatusForbidden || again.cookie != nil {
		t.Fatalf("second use: %d, cookie %v", r.code, again.cookie)
	}
	if r := again.get("/status"); r.code != http.StatusUnauthorized {
		t.Fatalf("page without session: %d", r.code)
	}
	late := u.launchURL()
	u.clock.Advance(LaunchTokenTTL)
	if r := newBrowser(t, late).do("GET", late, nil, nil); r.code != http.StatusForbidden {
		t.Fatalf("expired token: %d", r.code)
	}
	bad := strings.Split(late, "token=")[0] + "token=nope"
	if r := newBrowser(t, bad).do("GET", bad, nil, nil); r.code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", r.code)
	}
}

// Only 127.0.0.1:<port> and localhost:<port> are served (DNS rebinding).
func TestHostMustBeTheUIAddress(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	port := strings.TrimPrefix(b.base, "http://127.0.0.1:")
	for host, want := range map[string]int{
		"127.0.0.1:" + port:    http.StatusOK,
		"localhost:" + port:    http.StatusOK,
		"evil.example:" + port: http.StatusForbidden,
		"127.0.0.1":            http.StatusForbidden,
		"127.0.0.1:1":          http.StatusForbidden,
		"localhost.:" + port:   http.StatusForbidden,
	} {
		r := b.do("GET", "/status", nil, func(r *http.Request) { r.Host = host })
		if r.code != want {
			t.Errorf("Host %q: %d, want %d", host, r.code, want)
		}
	}
	launch := u.launchURL()
	r := newBrowser(t, launch).do("GET", launch, nil, func(r *http.Request) { r.Host = "evil.example:" + port })
	if r.code != http.StatusForbidden {
		t.Fatalf("launch on a rebound name: %d", r.code)
	}
}

// Every response, errors and static files included, is no-store and carries
// the security headers.
func TestEveryResponseIsNoStore(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	stranger := newBrowser(t, b.base)
	for name, r := range map[string]resp{
		"page":       b.get("/status"),
		"static":     b.get("/static/app.css"),
		"no session": stranger.get("/status"),
		"bad host":   b.do("GET", "/status", nil, func(r *http.Request) { r.Host = "evil.example" }),
		"used token": stranger.get("/launch?token=x"),
		"not found":  b.get("/nope"),
	} {
		if got := r.header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q", name, got)
		}
		if r.header.Get("Content-Security-Policy") == "" || r.header.Get("X-Frame-Options") != "DENY" || r.header.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers %v", name, r.header)
		}
	}
}

// A state-changing request needs the cookie, the session's CSRF token and
// a matching Origin; each missing piece is refused before the daemon is
// called.
func TestActionsNeedCookieCSRFAndOrigin(t *testing.T) {
	fd := statusDaemon(t)
	u := newUI(t, fd)
	b := u.open()
	other := u.open()
	token := b.csrf("/status")
	origin := func(o string) func(*http.Request) {
		return func(r *http.Request) {
			if o != "" {
				r.Header.Set("Origin", o)
			}
		}
	}
	noCookie := newBrowser(t, b.base)
	cases := []struct {
		name string
		b    *browser
		form url.Values
		edit func(*http.Request)
		want int
	}{
		{"no cookie", noCookie, url.Values{"csrf": {token}}, origin(b.base), http.StatusUnauthorized},
		{"no csrf", b, url.Values{}, origin(b.base), http.StatusForbidden},
		{"wrong csrf", b, url.Values{"csrf": {token + "x"}}, origin(b.base), http.StatusForbidden},
		{"another session's csrf", other, url.Values{"csrf": {token}}, origin(b.base), http.StatusForbidden},
		{"no origin", b, url.Values{"csrf": {token}}, origin(""), http.StatusForbidden},
		{"foreign origin", b, url.Values{"csrf": {token}}, origin("http://evil.example"), http.StatusForbidden},
		{"other port", b, url.Values{"csrf": {token}}, origin("http://127.0.0.1:1"), http.StatusForbidden},
		{"null origin", b, url.Values{"csrf": {token}}, origin("null"), http.StatusForbidden},
	}
	for _, tc := range cases {
		if r := tc.b.do("POST", "/status/kill", tc.form, tc.edit); r.code != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, r.code, tc.want)
		}
	}
	if n := len(fd.called(ipc.MethodKill)); n != 0 {
		t.Fatalf("kill called %d times by refused requests", n)
	}
	if r := b.do("GET", "/status/kill", nil, nil); r.code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on an action: %d", r.code)
	}
	r := b.do("POST", "/status/kill", url.Values{"csrf": {token}}, origin(b.base))
	if r.code != http.StatusSeeOther || r.header.Get("Location") != "/status" || len(fd.called(ipc.MethodKill)) != 1 {
		t.Fatalf("good request: %d to %q", r.code, r.header.Get("Location"))
	}
	wantContains(t, b.get("/status").body, "The kill switch is on. Every link is closed.", `action="/status/resume"`)
}

// The server stops IdleTimeout after the last request; a new ui.start
// starts a fresh one.
func TestIdleShutdownAfterThirtyMinutes(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	u.clock.Advance(IdleTimeout - time.Minute)
	u.l.sweep()
	if !u.l.Running() {
		t.Fatal("stopped before the idle timeout")
	}
	b.get("/status") // a request restarts the idle clock
	u.clock.Advance(IdleTimeout - time.Minute)
	u.l.sweep()
	if !u.l.Running() {
		t.Fatal("stopped although a request came in")
	}
	u.clock.Advance(time.Minute)
	u.l.sweep()
	if u.l.Running() {
		t.Fatal("still running 30 minutes after the last request")
	}
	if _, err := net.DialTimeout("tcp", strings.TrimPrefix(b.base, "http://"), time.Second); err == nil {
		t.Fatal("old port still accepts connections")
	}
	fresh := u.open()
	if r := fresh.get("/status"); r.code != http.StatusOK {
		t.Fatalf("after restart: %d", r.code)
	}
}

// The server stops with the daemon and refuses to start again.
func TestStopsWithTheDaemon(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	b := u.open()
	u.cancel()
	deadline := time.Now().Add(5 * time.Second)
	for u.l.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if u.l.Running() {
		t.Fatal("still running after the daemon stopped")
	}
	if _, err := u.l.Start(context.Background()); !errors.Is(err, ErrStopping) {
		t.Fatalf("start after stop: %v", err)
	}
	if _, err := net.DialTimeout("tcp", strings.TrimPrefix(b.base, "http://"), time.Second); err == nil {
		t.Fatal("port still accepts connections")
	}
}

// A password typed on the page unlocks only this browser session's
// connection; wrong passwords and the lockout are shown on the page.
func TestPasswordUnlocksOnlyThisBrowser(t *testing.T) {
	fd := statusDaemon(t)
	fd.setKilled(true)
	u := newUI(t, fd)
	a, b := u.open(), u.open()

	wantContains(t, a.follow("/status", "/status/resume", nil).body, "This needs your login password.")
	wantContains(t, a.follow("/status", "/status/resume", url.Values{"password": {"nope"}}).body, "Incorrect password.")
	page := a.follow("/status", "/status/resume", url.Values{"password": {"pw"}}).body
	wantContains(t, page, "Resumed.", "Password unlocked until 12:10 UTC", "Turn on the kill switch")

	fd.setKilled(true)
	wantContains(t, b.follow("/status", "/status/resume", nil).body, "This needs your login password.")
	if strings.Contains(b.get("/status").body, "Password unlocked") {
		t.Fatal("the other browser session shows as unlocked")
	}
	// a's window is still open: no password needed.
	wantContains(t, a.follow("/status", "/status/resume", nil).body, "Resumed.")

	fd.setKilled(true)
	for range 4 {
		b.follow("/status", "/status/resume", url.Values{"password": {"nope"}})
	}
	wantContains(t, b.follow("/status", "/status/resume", url.Values{"password": {"pw"}}).body,
		"Too many wrong passwords. Password actions are locked for 15 minutes.")
}

// Past MaxSessions the oldest browser session ends.
func TestOldestSessionEndsPastTheCap(t *testing.T) {
	u := newUI(t, statusDaemon(t))
	first := u.open()
	for range MaxSessions {
		u.open()
	}
	if r := first.get("/status"); r.code != http.StatusUnauthorized {
		t.Fatalf("oldest session: %d", r.code)
	}
}
```

Create `internal/webui/text_test.go`:

```go
package webui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/present"
)

func TestPeerTextUnwrapsAndCleans(t *testing.T) {
	body := "purpose: <b>train</b> & ‮evil‬\nnote: literal &lt; stays\tX\x1b[2J​\U000E0041"
	wrapped := present.Wrap(present.Item{Alias: "gpu-box", Session: "trainer", ID: "L1", Kind: "link", Body: body})
	got := peerText(wrapped)
	want := "purpose: <b>train</b> & evil\nnote: literal &lt; stays    X[2J"
	if got != want {
		t.Fatalf("peerText = %q\nwant       %q", got, want)
	}
	if peerText("") != "" {
		t.Fatal("empty wrapper")
	}
	if got := peerText("plain\x07 text"); got != "plain text" {
		t.Fatalf("unwrapped text: %q", got)
	}
}

func TestCleanLineRemovesHidingCharacters(t *testing.T) {
	in := "a\nb\rc\td\x1b​e⁦f g\U000E0067h\xff"
	if got := cleanLine(in); got != "abcdefgh�" {
		t.Fatalf("cleanLine = %q", got)
	}
}

// User-facing copy (templates, scripts, styles and the Go strings of this
// package) has no em dashes.
func TestUICopyHasNoEmDashes(t *testing.T) {
	fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(assets, p)
		if strings.ContainsRune(string(b), 0x2014) {
			t.Errorf("%s has an em dash", p)
		}
		return nil
	})
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(string(b), 0x2014) {
			t.Errorf("%s has an em dash", f)
		}
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/webui -count=1
```

Expected: FAIL (fails to compile), starting with:

```
internal/webui/harness_test.go:96:10: undefined: Launcher
internal/webui/harness_test.go:105:18: undefined: Caller
internal/webui/harness_test.go:109:60: undefined: NewLauncher
```

- [ ] **Step 3: Implement**

Create `internal/webui/launcher.go`:

```go
// Package webui is the local web UI: an HTTP server on 127.0.0.1 that the
// daemon starts on request (ui.start). It is a thin client over the IPC API.
// Every browser session holds its own in-process IPC connection, so the
// daemon's gates, tiers and password lockout apply unchanged, and a password
// unlock belongs to that browser session only.
package webui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Limits of the UI server.
const (
	// IdleTimeout stops the server this long after the last request.
	IdleTimeout = 30 * time.Minute
	// LaunchTokenTTL is how long a launch URL works (once).
	LaunchTokenTTL = 2 * time.Minute
	// MaxSessions caps browser sessions; the oldest is ended first.
	MaxSessions = 8
	// MaxLaunchTokens caps unused launch tokens; the oldest is dropped first.
	MaxLaunchTokens = 8
	// DefaultSweepEvery is how often the idle timeout is checked.
	DefaultSweepEvery = time.Minute
)

// ErrStopping is returned by Start once the daemon is shutting down.
var ErrStopping = errors.New("the daemon is stopping")

// Caller is one IPC connection to the daemon (satisfied by *ipc.Client).
type Caller interface {
	Call(ctx context.Context, method string, params, result any) error
	Close() error
}

// Dialer opens a new daemon connection. Each browser session gets its own.
type Dialer func() (Caller, error)

// Options configures a Launcher.
type Options struct {
	Clock core.Clock
	Dial  Dialer
	// Pages are the pages and actions to serve; nil serves DefaultRegistry().
	Pages  *Registry
	Logger *slog.Logger
	// SweepEvery is how often the idle timeout is checked (default
	// DefaultSweepEvery).
	SweepEvery time.Duration
	// Listen opens the listener (default: TCP on 127.0.0.1, random port).
	Listen func() (net.Listener, error)
}

// Launcher starts the UI server on demand and stops it when it has been idle
// for IdleTimeout or when the daemon's context ends. It implements
// api.UIPort.
type Launcher struct {
	o       Options
	mu      sync.Mutex
	stopped bool
	run     *running
}

// running is one server instance; a restart after an idle stop is a new one
// with a new port, new tokens and no sessions.
type running struct {
	srv  *server
	http *http.Server
	port int
	stop chan struct{}
	done chan struct{}
}

// NewLauncher returns a Launcher that stops for good when ctx ends.
func NewLauncher(ctx context.Context, o Options) *Launcher {
	if o.Clock == nil {
		o.Clock = core.SystemClock{}
	}
	if o.Pages == nil {
		o.Pages = DefaultRegistry()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.SweepEvery <= 0 {
		o.SweepEvery = DefaultSweepEvery
	}
	if o.Listen == nil {
		o.Listen = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	}
	l := &Launcher{o: o}
	context.AfterFunc(ctx, l.Stop)
	return l
}

// Start starts the server if it is not running and returns a launch URL
// with a new one-time token.
func (l *Launcher) Start(context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return "", ErrStopping
	}
	if l.run == nil {
		r, err := l.startLocked()
		if err != nil {
			return "", err
		}
		l.run = r
	}
	tok, err := l.run.srv.mintToken()
	if err != nil {
		return "", err
	}
	l.run.srv.touch()
	return fmt.Sprintf("http://127.0.0.1:%d/launch?token=%s", l.run.port, tok), nil
}

func (l *Launcher) startLocked() (*running, error) {
	ln, err := l.o.Listen()
	if err != nil {
		return nil, fmt.Errorf("start the web UI: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv, err := newServer(l.o, port)
	if err != nil {
		ln.Close()
		return nil, err
	}
	r := &running{
		srv: srv, port: port, stop: make(chan struct{}), done: make(chan struct{}),
		http: &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second},
	}
	go func() {
		defer close(r.done)
		if err := r.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			l.o.Logger.Warn("web UI stopped", "err", err)
		}
	}()
	go l.sweepLoop(r)
	l.o.Logger.Info("web UI started", "port", port)
	return r, nil
}

func (l *Launcher) sweepLoop(r *running) {
	t := time.NewTicker(l.o.SweepEvery)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			l.sweep()
		}
	}
}

// sweep stops the server once IdleTimeout has passed since the last request.
func (l *Launcher) sweep() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.run != nil && l.o.Clock.Now().Sub(l.run.srv.lastRequest()) >= IdleTimeout {
		l.o.Logger.Info("web UI idle, stopping")
		l.stopRunLocked()
	}
}

func (l *Launcher) stopRunLocked() {
	r := l.run
	l.run = nil
	close(r.stop)
	r.http.Close()
	<-r.done
	r.srv.closeSessions()
}

// Running reports whether the server is up.
func (l *Launcher) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.run != nil
}

// Stop stops the server and refuses later starts. The daemon's context
// ending calls it.
func (l *Launcher) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	if l.run != nil {
		l.stopRunLocked()
	}
}
```

Create `internal/webui/server.go`:

```go
package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// maxFormBytes caps a form body.
const maxFormBytes = 64 << 10

// contentSecurityPolicy allows only this origin's own scripts, styles and
// forms, and no framing.
const contentSecurityPolicy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// server is the HTTP handler of one running UI instance.
type server struct {
	clock  core.Clock
	dial   Dialer
	pages  *Registry
	log    *slog.Logger
	port   int
	cookie string
	hosts  [2]string
	views  *views
	mux    *http.ServeMux

	mu       sync.Mutex
	last     time.Time
	tokens   map[string]time.Time // launch token -> expiry
	sessions map[string]*uiSession
	order    []string // session IDs, oldest first
}

func newServer(o Options, port int) (*server, error) {
	v, err := loadViews()
	if err != nil {
		return nil, err
	}
	s := &server{
		clock: o.Clock, dial: o.Dial, pages: o.Pages, log: o.Logger, port: port,
		cookie: "cravv_ui_" + strconv.Itoa(port),
		hosts:  [2]string{"127.0.0.1:" + strconv.Itoa(port), "localhost:" + strconv.Itoa(port)},
		views:  v, last: o.Clock.Now(),
		tokens: map[string]time.Time{}, sessions: map[string]*uiSession{},
	}
	static, err := fs.Sub(assets, "assets/static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /launch", s.launch)
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /{$}", s.home)
	for _, p := range o.Pages.Pages() {
		mux.HandleFunc("GET "+p.Path, s.page(p))
	}
	for _, a := range o.Pages.Actions() {
		mux.HandleFunc("POST "+a.Path, s.action(a))
	}
	s.mux = mux
	return s, nil
}

// ServeHTTP runs the checks every request gets, then routes it.
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.touch()
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", contentSecurityPolicy)
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	if r.Host != s.hosts[0] && r.Host != s.hosts[1] {
		// A page on another name that resolves here (DNS rebinding) never
		// gets an answer.
		s.deny(w, http.StatusForbidden, "This address is not the cravv-connect UI.")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *server) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = s.clock.Now()
}

func (s *server) lastRequest() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// randomToken returns 32 random bytes, base64url.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// mintToken adds a launch token that works once within LaunchTokenTTL.
func (s *server) mintToken() (string, error) {
	tok, err := randomToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	for t, exp := range s.tokens {
		if !now.Before(exp) {
			delete(s.tokens, t)
		}
	}
	for len(s.tokens) >= MaxLaunchTokens {
		var oldest string
		for t, exp := range s.tokens {
			if oldest == "" || exp.Before(s.tokens[oldest]) {
				oldest = t
			}
		}
		delete(s.tokens, oldest)
	}
	s.tokens[tok] = now.Add(LaunchTokenTTL)
	return tok, nil
}

// takeToken consumes tok if it is a live launch token.
func (s *server) takeToken(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	for t, exp := range s.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(tok)) == 1 {
			delete(s.tokens, t)
			return now.Before(exp)
		}
	}
	return false
}

// launch swaps a launch token for a session cookie and redirects to a URL
// without the token.
func (s *server) launch(w http.ResponseWriter, r *http.Request) {
	if !s.takeToken(r.URL.Query().Get("token")) {
		s.deny(w, http.StatusForbidden, "This link was already used or has expired. Run cravv-connect ui again for a new one.")
		return
	}
	sess, err := s.newSession()
	if err != nil {
		s.log.Warn("web UI session", "err", err)
		s.deny(w, http.StatusServiceUnavailable, "Could not reach the daemon. Run cravv-connect ui again.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.cookie, Value: sess.id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// newSession opens a daemon connection for a new browser session, ending
// the oldest session beyond MaxSessions.
func (s *server) newSession() (*uiSession, error) {
	id, err := randomToken()
	if err != nil {
		return nil, err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, err
	}
	c, err := s.dial()
	if err != nil {
		return nil, err
	}
	sess := &uiSession{id: id, csrf: csrf, c: c, clock: s.clock}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[id] = sess
	s.order = append(s.order, id)
	for len(s.order) > MaxSessions {
		old := s.order[0]
		s.order = s.order[1:]
		if o, ok := s.sessions[old]; ok {
			o.c.Close()
			delete(s.sessions, old)
		}
	}
	return sess, nil
}

// session returns the browser session named by the request's cookie.
func (s *server) session(r *http.Request) *uiSession {
	ck, err := r.Cookie(s.cookie)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[ck.Value]
}

func (s *server) closeSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, sess := range s.sessions {
		sess.c.Close()
		delete(s.sessions, id)
	}
	s.order = nil
	clear(s.tokens)
}

// sameOrigin reports whether the request's Origin is this UI's origin.
// Browsers send Origin on every POST; a missing one is refused.
func (s *server) sameOrigin(r *http.Request) bool {
	return r.Header.Get("Origin") == "http://"+r.Host
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	if s.session(r) == nil {
		s.denyNoSession(w)
		return
	}
	pages := s.pages.Pages()
	if len(pages) == 0 {
		s.deny(w, http.StatusNotFound, "No pages.")
		return
	}
	http.Redirect(w, r, pages[0].Path, http.StatusSeeOther)
}

func (s *server) denyNoSession(w http.ResponseWriter) {
	s.deny(w, http.StatusUnauthorized, "This browser has no cravv-connect session, or it ended. Run cravv-connect ui in a terminal to open the UI.")
}

// deny answers with a plain text error.
func (s *server) deny(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintln(w, msg)
}

// page serves a page's GET view.
func (s *server) page(p Page) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.session(r)
		if sess == nil {
			s.denyNoSession(w)
			return
		}
		rq := &Request{HTTP: r, sess: sess}
		data, err := p.Load(r.Context(), rq)
		s.render(w, rq, p.Path, p.Title, p.Template, data, err)
	}
}

// action runs a state-changing POST: it needs the session cookie, the
// session's CSRF token and a matching Origin.
func (s *server) action(a Action) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := s.session(r)
		if sess == nil {
			s.denyNoSession(w)
			return
		}
		if !s.sameOrigin(r) {
			s.deny(w, http.StatusForbidden, "Cross-site request refused.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
		if err := r.ParseForm(); err != nil {
			s.deny(w, http.StatusBadRequest, "Bad form.")
			return
		}
		if !sess.csrfOK(r.PostForm.Get("csrf")) {
			s.deny(w, http.StatusForbidden, "Missing or wrong form token. Reload the page and try again.")
			return
		}
		rq := &Request{HTTP: r, sess: sess}
		rep, err := a.Run(r.Context(), rq)
		if err != nil {
			sess.addFlash(flashError, message(err))
			http.Redirect(w, r, a.Back, http.StatusSeeOther)
			return
		}
		if rep.Template != "" {
			s.render(w, rq, a.Back, rep.Title, rep.Template, rep.Data, nil)
			return
		}
		if rep.Notice != "" {
			sess.addFlash(flashOK, rep.Notice)
		}
		to := rep.To
		if to == "" {
			to = a.Back
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
	}
}
```

Create `internal/webui/session.go`:

```go
package webui

import (
	"crypto/subtle"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// Flash kinds.
const (
	flashOK    = "ok"
	flashError = "error"
)

// flash is a one-time message shown on the next page.
type flash struct {
	Kind string
	Text string
}

// uiSession is one browser session: its cookie ID, its CSRF token, its own
// daemon connection (which holds the password unlock) and pending messages.
type uiSession struct {
	id    string
	csrf  string
	c     Caller
	clock core.Clock

	mu       sync.Mutex
	flashes  []flash
	unlocked time.Time
}

func (s *uiSession) csrfOK(tok string) bool {
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.csrf)) == 1
}

func (s *uiSession) addFlash(kind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flashes = append(s.flashes, flash{Kind: kind, Text: text})
}

// takeFlashes returns and clears the pending messages.
func (s *uiSession) takeFlashes() []flash {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flashes
	s.flashes = nil
	return f
}

// setUnlocked records the end of the connection's password window.
func (s *uiSession) setUnlocked(until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlocked = until
}

// unlockedUntil returns the end of the password window, or zero if closed.
func (s *uiSession) unlockedUntil() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clock.Now().Before(s.unlocked) {
		return s.unlocked
	}
	return time.Time{}
}
```

Create `internal/webui/registry.go`:

```go
package webui

import (
	"context"
	"net/http"
	"strings"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// Page is one navigation entry and its GET view.
type Page struct {
	Path     string // such as "/devices"
	Title    string // navigation label and heading
	Template string // file under assets/templates that defines "content"
	// Load fetches the page's data over the browser session's IPC
	// connection. An error is shown above the page, which renders with the
	// data returned alongside it (often nil).
	Load func(ctx context.Context, rq *Request) (any, error)
}

// Action is one state-changing POST. The server checks the session cookie,
// the CSRF token and Origin before Run.
type Action struct {
	Path string // such as "/devices/pause"
	Back string // the page to return to, also after an error
	Run  func(ctx context.Context, rq *Request) (Reply, error)
}

// Reply is what an action returns: a redirect (to To, or Back) with an
// optional notice, or, when Template is set, a page rendered in place (for
// multi-step flows such as pairing).
type Reply struct {
	To       string
	Notice   string
	Template string
	Title    string
	Data     any
}

// Registry holds the pages and actions the UI serves. A new page is one new
// file with an add function plus one line in DefaultRegistry.
type Registry struct {
	pages   []Page
	actions []Action
}

// AddPage adds a page to the navigation, after the ones added before.
func (r *Registry) AddPage(p Page) { r.pages = append(r.pages, p) }

// AddAction adds a POST action.
func (r *Registry) AddAction(a Action) { r.actions = append(r.actions, a) }

// Pages returns the pages in navigation order.
func (r *Registry) Pages() []Page { return append([]Page(nil), r.pages...) }

// Actions returns the actions.
func (r *Registry) Actions() []Action { return append([]Action(nil), r.actions...) }

// DefaultRegistry returns the built-in pages in navigation order.
func DefaultRegistry() *Registry {
	r := &Registry{}
	for _, add := range []func(*Registry){
		addStatus,
	} {
		add(r)
	}
	return r
}

// Request is one page load or action for a browser session.
type Request struct {
	HTTP *http.Request
	sess *uiSession
}

// Call calls the daemon on this browser session's connection.
func (rq *Request) Call(ctx context.Context, method string, params, result any) error {
	return rq.sess.c.Call(ctx, method, params, result)
}

// Form returns a trimmed form value of a POST.
func (rq *Request) Form(name string) string {
	return strings.TrimSpace(rq.HTTP.PostFormValue(name))
}

// Query returns a trimmed query parameter.
func (rq *Request) Query(name string) string {
	return strings.TrimSpace(rq.HTTP.URL.Query().Get(name))
}

// WithPassword runs fn, an action the daemon gates behind the password. A
// password typed into the form's "password" field first unlocks this
// browser session's connection; the daemon's Guard checks it with the same
// rate limit and lockout as the CLI. Without one, fn runs as is and fails
// with the daemon's auth_required unless the window is still open.
func (rq *Request) WithPassword(ctx context.Context, fn func() error) error {
	if pw := rq.HTTP.PostFormValue("password"); pw != "" {
		var r ipc.UnlockResult
		if err := rq.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: pw}, &r); err != nil {
			return err
		}
		rq.sess.setUnlocked(r.ExpiresAt)
	}
	return fn()
}
```

Create `internal/webui/render.go`:

```go
package webui

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// assets holds the templates and static files; no build step, no external
// requests.
//
//go:embed assets
var assets embed.FS

// views holds one template set per page template: the layout plus the file
// that defines "content".
type views struct {
	byName map[string]*template.Template
}

var funcs = template.FuncMap{
	"time":  fmtTime,
	"short": shortID,
	"peer":  peerText,
	"clean": cleanLine,
	"dash":  dash,
}

func loadViews() (*views, error) {
	base, err := template.New("layout.html").Funcs(funcs).ParseFS(assets, "assets/templates/layout.html")
	if err != nil {
		return nil, err
	}
	files, err := fs.Glob(assets, "assets/templates/*.html")
	if err != nil {
		return nil, err
	}
	v := &views{byName: map[string]*template.Template{}}
	for _, f := range files {
		name := path.Base(f)
		if name == "layout.html" {
			continue
		}
		t, err := template.Must(base.Clone()).ParseFS(assets, f)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}
		v.byName[name] = t
	}
	return v, nil
}

type navItem struct {
	Path, Title string
	Current     bool
}

// view is what the layout renders.
type view struct {
	Title    string
	Nav      []navItem
	Flash    []flash
	Error    string
	CSRF     string
	Unlocked string
	Data     any
}

// render writes a page, or a plain 500 if the template fails.
func (s *server) render(w http.ResponseWriter, rq *Request, current, title, tmpl string, data any, loadErr error) {
	t, ok := s.views.byName[tmpl]
	if !ok {
		s.deny(w, http.StatusInternalServerError, "Unknown page.")
		return
	}
	v := view{Title: title, Flash: rq.sess.takeFlashes(), CSRF: rq.sess.csrf, Data: data}
	for _, p := range s.pages.Pages() {
		v.Nav = append(v.Nav, navItem{Path: p.Path, Title: p.Title, Current: p.Path == current})
	}
	if loadErr != nil {
		v.Error = message(loadErr)
	}
	if until := rq.sess.unlockedUntil(); !until.IsZero() {
		v.Unlocked = "Password unlocked until " + until.UTC().Format("15:04 UTC")
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", v); err != nil {
		s.log.Warn("web UI render", "template", tmpl, "err", err)
		s.deny(w, http.StatusInternalServerError, "The page could not be shown.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

// message is the text shown for an error. Daemon errors keep their own
// wording (they name what to do); the password ones are reworded for a page.
func message(err error) string {
	switch {
	case errors.Is(err, core.ErrBadPassword):
		return "Incorrect password."
	case errors.Is(err, core.ErrLocked):
		return fmt.Sprintf("Too many wrong passwords. Password actions are locked for %d minutes.", int(core.LockoutDuration.Minutes()))
	case errors.Is(err, core.ErrAuthRequired):
		return "This needs your login password."
	case errors.Is(err, core.ErrKilled):
		return "The kill switch is on. Resume on the Status page first."
	case errors.Is(err, ipc.ErrClosed):
		return "The connection to the daemon ended. Run cravv-connect ui again."
	}
	return cleanLine(err.Error())
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func shortID(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
```

Create `internal/webui/text.go`:

```go
package webui

import (
	"strings"
	"unicode"

	"github.com/cravv/cravv-connect/internal/present"
)

// cleanLine removes what could hide or reorder text on the page: control
// characters (newlines included), Unicode format characters (bidi controls,
// zero-width characters), line and paragraph separators, and the invisible
// characters present.Wrap strips. It is the CLI's terminal cleaning;
// html/template then escapes the result.
func cleanLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || present.IsInvisible(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

// xmlUnescaper undoes present.Wrap's body escaping; html/template escapes
// the text again for the page.
var xmlUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// peerText turns a <remote_message> wrapper (a view's Wrapped field) into
// plain text for the page: the wrapper lines are dropped (the page names the
// machine and session from validated fields), the body's escaping is undone
// and every line is cleaned. Tabs become four spaces.
func peerText(wrapped string) string {
	if wrapped == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(wrapped, "\r\n", "\n"), "\n")
	if n := len(lines); n >= 2 && strings.HasPrefix(lines[0], "<remote_message") && lines[n-1] == "</remote_message>" {
		lines = lines[1 : n-1]
	}
	for i, l := range lines {
		lines[i] = cleanLine(strings.ReplaceAll(xmlUnescaper.Replace(l), "\t", "    "))
	}
	return strings.Join(lines, "\n")
}
```

Create `internal/webui/pages_status.go`:

```go
package webui

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// addStatus is the Status page: machine status and the kill switch.
// Turning the kill switch on needs nothing; resuming needs the password.
func addStatus(r *Registry) {
	r.AddPage(Page{Path: "/status", Title: "Status", Template: "status.html", Load: loadStatus})
	r.AddAction(Action{Path: "/status/kill", Back: "/status", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		if err := rq.Call(ctx, ipc.MethodKill, nil, nil); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: "The kill switch is on. Every link is closed."}, nil
	}})
	r.AddAction(Action{Path: "/status/resume", Back: "/status", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		if err := rq.WithPassword(ctx, func() error { return rq.Call(ctx, ipc.MethodResume, nil, nil) }); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: "Resumed. Links must be requested again."}, nil
	}})
}

func loadStatus(ctx context.Context, rq *Request) (any, error) {
	var st ipc.StatusResult
	if err := rq.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
		return nil, err
	}
	return st, nil
}
```

Create `internal/webui/assets/templates/layout.html`:

```html
{{define "layout"}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · cravv-connect</title>
<link rel="stylesheet" href="/static/app.css">
<script src="/static/app.js" defer></script>
</head>
<body>
<header>
<span class="brand">cravv-connect</span>
<nav>{{range .Nav}}<a href="{{.Path}}"{{if .Current}} aria-current="page"{{end}}>{{.Title}}</a>{{end}}</nav>
{{if .Unlocked}}<span class="unlocked">{{.Unlocked}}</span>{{end}}
</header>
<main>
<h1>{{.Title}}</h1>
{{range .Flash}}<p class="flash {{.Kind}}" role="status">{{.Text}}</p>{{end}}
{{if .Error}}<p class="flash error" role="alert">{{.Error}}</p>{{end}}
{{template "content" .}}
</main>
</body>
</html>
{{end}}
{{define "password"}}<label class="password">Login password <input type="password" name="password" autocomplete="current-password"{{if .}} required{{end}}></label>{{end}}
```

Create `internal/webui/assets/templates/status.html`:

```html
{{define "content"}}{{$csrf := .CSRF}}{{with .Data}}
<section>
<h2>Kill switch</h2>
{{if .Killed}}
<p class="warn">The kill switch is on. Links are closed and nothing is sent or received.</p>
<form method="post" action="/status/resume">
<input type="hidden" name="csrf" value="{{$csrf}}">
{{template "password" (not $.Unlocked)}}
<button type="submit">Resume</button>
</form>
{{else}}
<p>Turning it on closes every link and stops all traffic until you resume with your password.</p>
<form method="post" action="/status/kill" data-confirm="Turn on the kill switch? Every link closes and nothing is sent or received until you resume.">
<input type="hidden" name="csrf" value="{{$csrf}}">
<button type="submit" class="danger">Turn on the kill switch</button>
</form>
{{end}}
</section>
<section>
<h2>This machine</h2>
<dl>
<dt>Device name</dt><dd>{{dash .DeviceName}}</dd>
<dt>Machine ID</dt><dd><code>{{.MachineID}}</code></dd>
<dt>Relay</dt><dd>{{dash .RelayURL}} ({{if .RelayConnected}}connected{{else}}not connected{{end}})</dd>
<dt>Shared sessions</dt><dd>{{range $i, $s := .Sessions}}{{if $i}}, {{end}}{{$s}}{{else}}none{{end}}</dd>
<dt>Unread messages</dt><dd>{{.InboxUnread}}</dd>
<dt>Tasks awaiting approval</dt><dd>{{.PendingApprovals}}</dd>
<dt>Outbox</dt><dd>{{.OutboxPending}} pending, {{.OutboxHeld}} held</dd>
</dl>
</section>
{{if .Errors}}
<section>
<h2>Problems</h2>
<ul>{{range .Errors}}<li>{{clean .}}</li>{{end}}</ul>
</section>
{{end}}
{{end}}{{end}}
```

Create `internal/webui/assets/static/app.css`:

```css
:root {
  --bg: #fafafa;
  --fg: #1d1d1f;
  --muted: #6e6e73;
  --line: #d2d2d7;
  --accent: #0a60c2;
  --ok: #1e7b34;
  --bad: #b3261e;
  --panel: #ffffff;
  color-scheme: light dark;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #161618;
    --fg: #ececf0;
    --muted: #a1a1a6;
    --line: #3a3a3f;
    --accent: #6ab0ff;
    --ok: #6fd08c;
    --bad: #ff8a80;
    --panel: #1f1f22;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--fg);
  font: 15px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
}
header {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 16px;
  padding: 12px 16px;
  border-bottom: 1px solid var(--line);
  background: var(--panel);
}
.brand { font-weight: 600; }
nav { display: flex; flex-wrap: wrap; gap: 12px; }
nav a { color: var(--accent); text-decoration: none; }
nav a[aria-current="page"] { font-weight: 600; text-decoration: underline; }
.unlocked { margin-left: auto; color: var(--muted); font-size: 13px; }
main { max-width: 960px; margin: 0 auto; padding: 16px; }
h1 { font-size: 22px; margin: 8px 0 16px; }
h2 { font-size: 17px; margin: 24px 0 8px; }
section {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 12px 16px;
  margin-bottom: 16px;
}
table { width: 100%; border-collapse: collapse; }
th, td { text-align: left; padding: 6px 8px; border-bottom: 1px solid var(--line); vertical-align: top; }
th { color: var(--muted); font-weight: 500; font-size: 13px; }
.table-wrap { overflow-x: auto; }
form { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 6px; margin: 2px 0; }
form.block { display: flex; margin: 8px 0; }
input, select, button { font: inherit; padding: 4px 8px; border-radius: 6px; border: 1px solid var(--line); background: var(--bg); color: var(--fg); }
button { cursor: pointer; background: var(--accent); color: #fff; border-color: var(--accent); }
button.secondary { background: transparent; color: var(--accent); }
button.danger { background: var(--bad); border-color: var(--bad); }
label.password { display: inline-flex; gap: 6px; align-items: center; }
.flash { padding: 8px 12px; border-radius: 6px; border: 1px solid var(--line); }
.flash.ok { border-color: var(--ok); color: var(--ok); }
.flash.error { border-color: var(--bad); color: var(--bad); }
.warn { color: var(--bad); font-weight: 600; }
.muted { color: var(--muted); }
pre.peer {
  white-space: pre-wrap;
  word-break: break-word;
  margin: 4px 0;
  padding: 6px 8px;
  border-left: 3px solid var(--line);
  color: var(--muted);
  font-size: 13px;
}
.code { font: 600 28px/1.2 ui-monospace, SFMono-Regular, Menlo, monospace; letter-spacing: 2px; }
dl { display: grid; grid-template-columns: max-content 1fr; gap: 4px 16px; margin: 0; }
dt { color: var(--muted); }
dd { margin: 0; word-break: break-word; }
```

Create `internal/webui/assets/static/app.js`:

```js
// cravv-connect web UI. Everything works without this script; it only asks
// before drastic actions and starts waits a page asks for.
"use strict";

document.addEventListener("submit", function (e) {
  var msg = e.target.getAttribute("data-confirm");
  if (msg && !window.confirm(msg)) {
    e.preventDefault();
  }
});

document.addEventListener("DOMContentLoaded", function () {
  var form = document.querySelector("form[data-autosubmit]");
  if (form) {
    form.submit();
  }
});
```

- [ ] **Step 4: Run the tests to see them pass**

```bash
go test ./internal/webui -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: every `internal/webui` test PASSES (`TestLaunchSwapsTokenForCookie`, `TestLaunchTokenWorksOnceAndExpires`, `TestHostMustBeTheUIAddress`, `TestEveryResponseIsNoStore`, `TestActionsNeedCookieCSRFAndOrigin`, `TestIdleShutdownAfterThirtyMinutes`, `TestStopsWithTheDaemon`, `TestPasswordUnlocksOnlyThisBrowser`, `TestOldestSessionEndsPastTheCap`, `TestPeerTextUnwrapsAndCleans`, `TestCleanLineRemovesHidingCharacters`, `TestUICopyHasNoEmDashes`), `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/webui
git commit -m "webui: launcher, request checks, browser sessions and the Status page

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: webui: Devices page with pairing, and the Activity page

Devices lists paired machines (status, last seen from Task 2, paired time) with pause, resume and unpair, and runs pairing on the page: the bind code (`pair.start`), the wait for the other device (`pair.await`, a form the script submits at once), or joining with a code (`join.start`), then naming the device (`pair.finalize`). Activity shows the audit tail newest first.

**Files:**
- Create: `internal/webui/pages_devices.go`, `internal/webui/pages_activity.go`, `internal/webui/assets/templates/devices.html`, `internal/webui/assets/templates/pair.html`, `internal/webui/assets/templates/pairname.html`, `internal/webui/assets/templates/activity.html`
- Modify: `internal/webui/registry.go`
- Test: `internal/webui/pages_devices_test.go` (new)

**Interfaces:**

Consumes: Task 4's `Registry`, `Request`, `Reply`, `cleanLine`, `shortID`; `ipc` pairing, peer and audit methods; `audit.Event`.

Produces:

```go
// internal/webui/pages_activity.go
const ActivityLimit = 200
```

**Design notes:**
- Each pairing step is an action that renders its next step in place (`Reply.Template`), so the pending ID travels only in the page's own form; the IPC connection stays unlocked from `pair.start` for `core.UnlockTTL`, and the name form accepts the password again if that window closed.
- The other device's suggested name is peer-chosen: it is turned into a valid alias with the CLI's rule (`suggestAlias`) before it is shown.

- [ ] **Step 1: Write the failing tests**

Create `internal/webui/pages_devices_test.go`:

```go
package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

func devicesDaemon(t *testing.T) *fakeDaemon {
	fd := newFakeDaemon(t)
	seen := time.Date(2026, 9, 26, 11, 58, 0, 0, time.UTC)
	fd.reply(ipc.MethodMachines, ipc.GateAllowWhenKilled, ipc.PeerListResult{Peers: []ipc.PeerView{
		{Alias: "gpu-box", MachineID: "GPUMACHINEID0000000000", Online: true, LastSeen: seen, PairedAt: seen.Add(-time.Hour)},
		{Alias: "old-mac", MachineID: "OLDMAC", Paused: true},
	}})
	for _, m := range []string{ipc.MethodPeerPause, ipc.MethodPeerResume, ipc.MethodPeerUnpair} {
		fd.reply(m, ipc.GateNone, nil)
	}
	fd.reply(ipc.MethodPairStart, ipc.GateUnlock, ipc.PairStartResult{PendingID: "P1", Code: "ABCD-2345"})
	proposal := ipc.PendingPeerResult{PendingID: "P1", SuggestedName: "GPU Box <b>!", MachineID: "NEWMACHINEID00000000"}
	fd.reply(ipc.MethodPairAwait, ipc.GateUnlock, proposal)
	fd.reply(ipc.MethodJoinStart, ipc.GateUnlock, proposal)
	fd.handle(ipc.MethodPairFinalize, ipc.GateUnlock, func(_ *ipc.ConnState, p json.RawMessage) (any, error) {
		var fp ipc.PairFinalizeParams
		json.Unmarshal(p, &fp)
		return ipc.PairFinalizeResult{Alias: fp.Alias}, nil
	})
	return fd
}

func TestDevicesPageListsMachines(t *testing.T) {
	b := newUI(t, devicesDaemon(t)).open()
	page := b.get("/devices").body
	wantContains(t, page,
		"<td>gpu-box</td>", "<code>GPUMACHINEID0000</code>", "<td>online</td>", "<td>2026-09-26 11:58 UTC</td>",
		"<td>old-mac</td>", "<td>paused by you</td>", `action="/devices/resume"`, `action="/devices/pause"`,
		`data-confirm="Unpair gpu-box? Its links close, and it must pair again to reconnect."`,
		`action="/devices/pair"`, `action="/devices/join"`)
}

// Pause, resume and unpair need no password (the daemon's gates), and name
// the device by its alias.
func TestDevicesPauseResumeUnpair(t *testing.T) {
	fd := devicesDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/devices", "/devices/pause", url.Values{"alias": {"gpu-box"}}).body, "Paused gpu-box.")
	wantContains(t, b.follow("/devices", "/devices/resume", url.Values{"alias": {"old-mac"}}).body, "Resumed old-mac.")
	wantContains(t, b.follow("/devices", "/devices/unpair", url.Values{"alias": {"gpu-box"}}).body, "Unpaired gpu-box.")
	if got := fd.called(ipc.MethodPeerUnpair); len(got) != 1 || got[0] != `{"alias":"gpu-box"}` {
		t.Fatalf("unpair calls %v", got)
	}
}

// Pairing on the page: the password unlocks, the bind code shows, the wait
// form (submitted by the script, or by hand) returns the other device, and
// naming it finishes the pairing.
func TestPairNewDeviceOnThePage(t *testing.T) {
	fd := devicesDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/devices", "/devices/pair", nil).body, "This needs your login password.")

	r := b.post("/devices", "/devices/pair", url.Values{"password": {"pw"}})
	if r.code != http.StatusOK {
		t.Fatalf("pair: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, `<p class="code">ABCD-2345</p>`, "cravv-connect join ABCD-2345",
		`action="/devices/pair/wait" data-autosubmit`, `name="pending_id" value="P1"`)

	r = b.post("/devices", "/devices/pair/wait", url.Values{"pending_id": {"P1"}})
	if r.code != http.StatusOK {
		t.Fatalf("wait: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, "<code>NEWMACHINEID0000</code>", `name="alias" value="gpu-box-b"`)
	if strings.Contains(r.body, "<b>") {
		t.Fatal("the other device's suggested name reached the page unescaped")
	}
	page := b.follow("/devices", "/devices/pair/finish", url.Values{"pending_id": {"P1"}, "alias": {"gpu"}}).body
	wantContains(t, page, "Paired with gpu. Its sessions can now ask to link with yours; you decide each link.")
	if got := fd.called(ipc.MethodPairFinalize); len(got) != 1 || got[0] != `{"pending_id":"P1","alias":"gpu"}` {
		t.Fatalf("finalize calls %v", got)
	}
}

func TestJoinWithACodeOnThePage(t *testing.T) {
	fd := devicesDaemon(t)
	b := newUI(t, fd).open()
	r := b.post("/devices", "/devices/join", url.Values{"code": {" ABCD-2345 "}, "password": {"pw"}})
	if r.code != http.StatusOK {
		t.Fatalf("join: %d %s", r.code, r.body)
	}
	wantContains(t, r.body, `action="/devices/pair/finish"`, `name="pending_id" value="P1"`)
	if got := fd.called(ipc.MethodJoinStart); len(got) != 1 || got[0] != `{"code":"ABCD-2345"}` {
		t.Fatalf("join calls %v", got)
	}
}

// The Activity page shows the audit tail newest first, with every value
// escaped and cleaned.
func TestActivityPageShowsAuditTail(t *testing.T) {
	fd := newFakeDaemon(t)
	t0 := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	fd.reply(ipc.MethodAuditRead, ipc.GateAllowWhenKilled, ipc.AuditReadResult{Events: []audit.Event{
		{TS: t0, Type: audit.EvPair, Alias: "gpu-box"},
		{TS: t0.Add(time.Minute), Type: audit.EvLinkReject, Peer: core.MachineID("PEERMACHINEID0000000"), ItemID: "L1",
			Detail: map[string]any{"reason": "<script>x</script>‮", "direction": "in"}},
	}})
	page := newUI(t, fd).open().get("/activity").body
	newer := strings.Index(page, "link_reject")
	older := strings.Index(page, "<td>pair</td>")
	if newer < 0 || older < 0 || newer > older {
		t.Fatalf("not newest first:\n%s", page)
	}
	wantContains(t, page, "<td>PEERMACHINEID000</td>", "direction=in, reason=&lt;script&gt;x&lt;/script&gt;", "2026-09-26 09:01 UTC")
	if strings.Contains(page, "<script>x") || strings.ContainsRune(page, 0x202e) {
		t.Fatal("audit detail reached the page raw")
	}
	if got := fd.called(ipc.MethodAuditRead); len(got) != 1 || got[0] != `{"limit":200}` {
		t.Fatalf("audit.read calls %v", got)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/webui -run '^(TestDevicesPageListsMachines|TestDevicesPauseResumeUnpair|TestPairNewDeviceOnThePage|TestJoinWithACodeOnThePage|TestActivityPageShowsAuditTail)$' -count=1
```

Expected: FAIL; all five tests fail because `/devices`, its actions and `/activity` are not registered yet (the routes answer `404 page not found`).

- [ ] **Step 3: Implement**

Create `internal/webui/pages_devices.go`:

```go
package webui

import (
	"context"
	"regexp"
	"strings"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// addDevices is the Devices page: paired machines, pause, resume, unpair
// and pairing a new device. Pairing needs the password (the daemon gates
// pair.start, pair.await, join.start and pair.finalize).
func addDevices(r *Registry) {
	r.AddPage(Page{Path: "/devices", Title: "Devices", Template: "devices.html", Load: loadDevices})
	peerAction := func(path, method, done string) {
		r.AddAction(Action{Path: path, Back: "/devices", Run: func(ctx context.Context, rq *Request) (Reply, error) {
			alias := rq.Form("alias")
			if err := rq.Call(ctx, method, ipc.AliasParams{Alias: alias}, nil); err != nil {
				return Reply{}, err
			}
			return Reply{Notice: done + " " + alias + "."}, nil
		}})
	}
	peerAction("/devices/pause", ipc.MethodPeerPause, "Paused")
	peerAction("/devices/resume", ipc.MethodPeerResume, "Resumed")
	peerAction("/devices/unpair", ipc.MethodPeerUnpair, "Unpaired")
	r.AddAction(Action{Path: "/devices/pair", Back: "/devices", Run: pairStart})
	r.AddAction(Action{Path: "/devices/pair/wait", Back: "/devices", Run: pairWait})
	r.AddAction(Action{Path: "/devices/join", Back: "/devices", Run: joinStart})
	r.AddAction(Action{Path: "/devices/pair/finish", Back: "/devices", Run: pairFinish})
}

func loadDevices(ctx context.Context, rq *Request) (any, error) {
	var r ipc.PeerListResult
	if err := rq.Call(ctx, ipc.MethodMachines, nil, &r); err != nil {
		return nil, err
	}
	return r, nil
}

// pairCode is the step that shows the bind code and waits.
type pairCode struct {
	PendingID string
	Code      string
}

// pairName is the step that names the new device.
type pairName struct {
	PendingID string
	MachineID string
	Suggested string
}

func pairStart(ctx context.Context, rq *Request) (Reply, error) {
	var start ipc.PairStartResult
	if err := rq.WithPassword(ctx, func() error { return rq.Call(ctx, ipc.MethodPairStart, nil, &start) }); err != nil {
		return Reply{}, err
	}
	return Reply{Template: "pair.html", Title: "Pair a new device", Data: pairCode{PendingID: start.PendingID, Code: start.Code}}, nil
}

// pairWait blocks until the other device joins with the code.
func pairWait(ctx context.Context, rq *Request) (Reply, error) {
	var p ipc.PendingPeerResult
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: rq.Form("pending_id")}, &p)
	}); err != nil {
		return Reply{}, err
	}
	return nameStep(p), nil
}

func joinStart(ctx context.Context, rq *Request) (Reply, error) {
	var p ipc.PendingPeerResult
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: rq.Form("code")}, &p)
	}); err != nil {
		return Reply{}, err
	}
	return nameStep(p), nil
}

func nameStep(p ipc.PendingPeerResult) Reply {
	return Reply{Template: "pairname.html", Title: "Name the new device", Data: pairName{
		PendingID: p.PendingID, MachineID: cleanLine(p.MachineID), Suggested: suggestAlias(p.SuggestedName),
	}}
}

func pairFinish(ctx context.Context, rq *Request) (Reply, error) {
	var res ipc.PairFinalizeResult
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodPairFinalize, ipc.PairFinalizeParams{PendingID: rq.Form("pending_id"), Alias: rq.Form("alias")}, &res)
	}); err != nil {
		return Reply{}, err
	}
	return Reply{To: "/devices", Notice: "Paired with " + res.Alias + ". Its sessions can now ask to link with yours; you decide each link."}, nil
}

var aliasUnsafe = regexp.MustCompile(`[^a-z0-9-]+`)

// suggestAlias turns the name the other device suggests (chosen by that
// device) into a valid local alias, as the CLI does.
func suggestAlias(s string) string {
	s = aliasUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 24 {
		s = strings.TrimRight(s[:24], "-")
	}
	if s == "" {
		return "peer"
	}
	return s
}
```

Create `internal/webui/pages_activity.go`:

```go
package webui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// ActivityLimit is how many audit events the Activity page shows.
const ActivityLimit = 200

// addActivity is the Activity page: the audit log tail, newest first.
func addActivity(r *Registry) {
	r.AddPage(Page{Path: "/activity", Title: "Activity", Template: "activity.html", Load: loadActivity})
}

type activityRow struct {
	At     time.Time
	Type   string
	Who    string
	Item   string
	Detail string
}

func loadActivity(ctx context.Context, rq *Request) (any, error) {
	var r ipc.AuditReadResult
	if err := rq.Call(ctx, ipc.MethodAuditRead, ipc.AuditReadParams{Limit: ActivityLimit}, &r); err != nil {
		return nil, err
	}
	rows := make([]activityRow, 0, len(r.Events))
	for i := len(r.Events) - 1; i >= 0; i-- {
		e := r.Events[i]
		who := e.Alias
		if who == "" {
			who = shortID(string(e.Peer))
		}
		keys := make([]string, 0, len(e.Detail))
		for k := range e.Detail {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for j, k := range keys {
			parts[j] = fmt.Sprintf("%s=%v", k, e.Detail[k])
		}
		rows = append(rows, activityRow{
			At: e.TS, Type: cleanLine(e.Type), Who: cleanLine(who), Item: cleanLine(e.ItemID), Detail: cleanLine(strings.Join(parts, ", ")),
		})
	}
	return rows, nil
}
```

Create `internal/webui/assets/templates/devices.html`:

```html
{{define "content"}}{{$csrf := .CSRF}}
<section>
<h2>Paired devices</h2>
{{with .Data}}{{if .Peers}}
<div class="table-wrap"><table>
<thead><tr><th>Name</th><th>Machine</th><th>Status</th><th>Last seen</th><th>Paired</th><th></th></tr></thead>
<tbody>
{{range .Peers}}<tr>
<td>{{.Alias}}</td>
<td><code>{{short .MachineID}}</code></td>
<td>{{if .Paused}}paused by you{{else if .PausedByPeer}}paused by them{{else if .Online}}online{{else}}offline{{end}}</td>
<td>{{time .LastSeen}}</td>
<td>{{time .PairedAt}}</td>
<td>
{{if .Paused}}<form method="post" action="/devices/resume"><input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="alias" value="{{.Alias}}"><button type="submit">Resume</button></form>
{{else}}<form method="post" action="/devices/pause"><input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="alias" value="{{.Alias}}"><button type="submit" class="secondary">Pause</button></form>
{{end}}<form method="post" action="/devices/unpair" data-confirm="Unpair {{.Alias}}? Its links close, and it must pair again to reconnect."><input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="alias" value="{{.Alias}}"><button type="submit" class="danger">Unpair</button></form>
</td>
</tr>{{end}}
</tbody>
</table></div>
{{else}}<p class="muted">No paired devices yet.</p>{{end}}{{end}}
</section>
<section>
<h2>Pair a new device</h2>
<p>Creates a one-time bind code for the other device. It works once and expires in 10 minutes.</p>
<form method="post" action="/devices/pair" class="block">
<input type="hidden" name="csrf" value="{{$csrf}}">
{{template "password" (not $.Unlocked)}}
<button type="submit">Create a bind code</button>
</form>
<h2>Join with a code</h2>
<p>Enter the bind code the other device shows.</p>
<form method="post" action="/devices/join" class="block">
<input type="hidden" name="csrf" value="{{$csrf}}">
<label>Bind code <input name="code" required autocomplete="off"></label>
{{template "password" (not $.Unlocked)}}
<button type="submit">Join</button>
</form>
</section>
{{end}}
```

Create `internal/webui/assets/templates/pair.html`:

```html
{{define "content"}}{{$csrf := .CSRF}}{{with .Data}}
<section>
<p>On the other device, join with this code in its cravv-connect UI, or run:</p>
<p class="code">{{.Code}}</p>
<pre>cravv-connect join {{.Code}}</pre>
<p>The code works once and expires in 10 minutes.</p>
<form method="post" action="/devices/pair/wait" data-autosubmit>
<input type="hidden" name="csrf" value="{{$csrf}}">
<input type="hidden" name="pending_id" value="{{.PendingID}}">
<button type="submit">Wait for the other device</button>
</form>
<p class="muted">Waiting for the other device. Keep this page open.</p>
</section>
{{end}}{{end}}
```

Create `internal/webui/assets/templates/pairname.html`:

```html
{{define "content"}}{{$csrf := .CSRF}}{{with .Data}}
<section>
<p>Connected to machine <code>{{short .MachineID}}</code>. Choose the name this machine will use for it.</p>
<form method="post" action="/devices/pair/finish" class="block">
<input type="hidden" name="csrf" value="{{$csrf}}">
<input type="hidden" name="pending_id" value="{{.PendingID}}">
<label>Name <input name="alias" value="{{.Suggested}}" required maxlength="24" autocomplete="off"></label>
{{template "password" (not $.Unlocked)}}
<button type="submit">Pair</button>
</form>
<p class="muted">Pairing lets the two machines see shared sessions and ask for links. You decide each link.</p>
</section>
{{end}}{{end}}
```

Create `internal/webui/assets/templates/activity.html`:

```html
{{define "content"}}
<section>
{{if .Data}}
<div class="table-wrap"><table>
<thead><tr><th>Time</th><th>Event</th><th>Device</th><th>Item</th><th>Detail</th></tr></thead>
<tbody>
{{range .Data}}<tr><td>{{time .At}}</td><td>{{.Type}}</td><td>{{dash .Who}}</td><td>{{dash .Item}}</td><td>{{.Detail}}</td></tr>
{{end}}
</tbody>
</table></div>
{{else}}<p class="muted">No activity recorded yet.</p>{{end}}
</section>
{{end}}
```

In `internal/webui/registry.go`, `DefaultRegistry`, replace the list:

```go
	for _, add := range []func(*Registry){
		addStatus,
	} {
```

with:

```go
	for _, add := range []func(*Registry){
		addDevices,
		addActivity,
		addStatus,
	} {
```

- [ ] **Step 4: Run the tests to see them pass**

```bash
go test ./internal/webui -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: every `internal/webui` test PASSES (Task 4's `TestLaunchSwapsTokenForCookie` now follows `/` to `/devices`), `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/webui
git commit -m "webui: Devices page with pairing, and the Activity page

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: webui: Sessions page with connect, disconnect and restrict, and the Approvals page

Sessions shows this machine's shared sessions (`sessions.local`), its links (`links`) with disconnect and restrict (to a lower level only), and, per paired device, the sessions that device lets this machine see (`sessions.list`, loaded on demand with `?machine=`), each with a Connect form (`link.connect_as`, password). Approvals shows link requests waiting for this side (accept with the password at a chosen level, reject with nothing) and the tasks-ask tasks (`approvals.list`, behind the password, then approve or deny).

**Files:**
- Create: `internal/webui/links.go`, `internal/webui/pages_sessions.go`, `internal/webui/pages_approvals.go`, `internal/webui/assets/templates/sessions.html`, `internal/webui/assets/templates/approvals.html`
- Modify: `internal/webui/registry.go`
- Test: `internal/webui/pages_sessions_test.go` (new), `internal/webui/pages_approvals_test.go` (new)

**Interfaces:**

Consumes: Task 4's page registry and text helpers; `ipc.LinkView`, `ipc.RemoteSessionView`, `ipc.ApprovalView`, `core.Permission`; Task 3's `sessions.local` and `link.connect_as`.

Produces: no exported API (pages only).

**Design notes:**
- Pending inbound requests are listed only on Approvals; the Sessions page counts them and links there. Closed links show why and offer no action.
- Restrict offers only the levels below the link's `permission_in` (lowering needs nothing); raising stays with the CLI's `link permit` (password).
- Connect offers only open local sessions. The link view's `Wrapped` purpose and note, the remote session's purpose and task instructions are shown with `peerText` or `peerLines` inside `<pre class="peer">`.
- While the kill switch is on the pages still render: what the daemon refuses is replaced by its message.

- [ ] **Step 1: Write the failing tests**

Create `internal/webui/pages_sessions_test.go`:

```go
package webui

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
)

// hostile is peer-chosen text that tries to break out of the page, hide
// itself and reorder what the human reads.
const hostile = "</pre><script>alert(1)</script><img src=x onerror=alert(2)>‮evil​"

func wrapped(kind, body string) string {
	return present.Wrap(present.Item{Alias: "gpu-box", Session: "trainer", ID: "X1", Kind: kind, Body: body})
}

var sampleLinkViews = []ipc.LinkView{
	{Link: 1, Machine: "gpu-box", Session: "lead", RemoteSession: "trainer", Direction: "out", State: "active",
		PermissionIn: "tasks-auto", PermissionOut: "tasks-ask", Wrapped: wrapped("link", "purpose: "+hostile)},
	{Link: 2, Machine: "mac", Session: "lead", RemoteSession: "helper", Direction: "in", State: "pending", Proposed: "tasks-ask",
		Wrapped: wrapped("link", "note: please link "+hostile)},
	{Link: 3, Machine: "gpu-box", Session: "lead", RemoteSession: "old", Direction: "out", State: "closed", Reason: "presence_timeout", PermissionIn: "messages"},
	{Link: 4, Machine: "gpu-box", Session: "side", RemoteSession: "eval", Direction: "in", State: "active", PermissionIn: "messages", RemoteAway: true},
}

func sessionsDaemon(t *testing.T) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodSessionsLocal, ipc.GateAllowWhenKilled, ipc.LocalSessionsResult{Sessions: []ipc.SharedSessionView{
		{Name: "lead", Purpose: "coordinate the run", Visibility: "all-peers", State: "open", Kind: "live", Agent: "claude"},
		{Name: "side", Visibility: "peers:gpu-box", State: "open", Kind: "live", Agent: "codex"},
		{Name: "nap", Visibility: "private", State: "away", Kind: "live", Agent: "claude"},
	}})
	fd.reply(ipc.MethodLinks, ipc.GateNone, ipc.LinksResult{Links: sampleLinkViews})
	fd.reply(ipc.MethodMachines, ipc.GateAllowWhenKilled, ipc.PeerListResult{Peers: []ipc.PeerView{{Alias: "gpu-box"}, {Alias: "mac"}}})
	fd.reply(ipc.MethodSessionsList, ipc.GateNone, ipc.SessionsListResult{Machine: "gpu-box", Sessions: []ipc.RemoteSessionView{
		{Name: "trainer", Kind: "live", Agent: "claude", State: "open", Wrapped: wrapped("session", "purpose: "+hostile)},
	}})
	fd.handle(ipc.MethodLinkConnectAs, ipc.GateUnlock, func(*ipc.ConnState, json.RawMessage) (any, error) {
		return ipc.LinkView{Link: 5, State: "pending"}, nil
	})
	fd.reply(ipc.MethodLinkDisconnect, ipc.GateNone, nil)
	fd.handle(ipc.MethodLinkRestrict, ipc.GateNone, func(_ *ipc.ConnState, p json.RawMessage) (any, error) {
		var lp ipc.LinkPermissionParams
		json.Unmarshal(p, &lp)
		return ipc.LinkView{Link: lp.Link, PermissionIn: lp.Permission}, nil
	})
	return fd
}

func TestSessionsPageShowsLocalSessionsAndLinks(t *testing.T) {
	b := newUI(t, sessionsDaemon(t)).open()
	page := b.get("/sessions").body
	wantContains(t, page,
		"<td>lead</td><td>coordinate the run</td><td>all-peers</td><td>open</td><td>claude</td>",
		"<td>nap</td><td>-</td><td>private</td><td>away</td>",
		"<td>1</td><td>lead</td><td>gpu-box/trainer",
		"<td>active</td><td>tasks-auto</td><td>tasks-ask</td>",
		`<option value="messages">messages</option><option value="tasks-ask">tasks-ask</option></select><button type="submit" class="secondary">Restrict</button>`,
		"<td>closed: presence_timeout</td>", "<td>active (peer away)</td>",
		"Link requests waiting for you: 1.",
		`href="/sessions?machine=gpu-box"`, `href="/sessions?machine=mac"`)
	if strings.Contains(page, `name="link" value="3"`) {
		t.Fatal("a closed link offers actions")
	}
	if strings.Contains(page, "mac/helper") {
		t.Fatal("a pending request is listed with the links; it belongs on the Approvals page")
	}
}

// Peer-chosen text is escaped by html/template and cleaned of hiding and
// reordering characters before it reaches the page.
func TestPeerStringsAreEscapedAndCleaned(t *testing.T) {
	b := newUI(t, sessionsDaemon(t)).open()
	for _, page := range []string{b.get("/sessions?machine=gpu-box").body, b.get("/approvals").body} {
		if strings.Contains(page, "<script>alert") || strings.Contains(page, "<img src=x") || strings.Contains(page, "</pre><script") {
			t.Fatalf("peer markup reached the page:\n%s", page)
		}
		if strings.ContainsRune(page, 0x202E) || strings.ContainsRune(page, 0x200B) {
			t.Fatal("hiding characters reached the page")
		}
		wantContains(t, page, "&lt;/pre&gt;&lt;script&gt;alert(1)&lt;/script&gt;&lt;img src=x onerror=alert(2)&gt;evil")
	}
}

// Asking for a link on a session's behalf needs the password and sends the
// chosen session, target, permission and note.
func TestConnectFromTheSessionsPage(t *testing.T) {
	fd := sessionsDaemon(t)
	b := newUI(t, fd).open()
	page := b.get("/sessions?machine=gpu-box").body
	wantContains(t, page, "<h2>gpu-box</h2>", `name="remote" value="trainer"`,
		`<option value="lead">lead</option><option value="side">side</option></select>`)
	if strings.Contains(page, `<option value="nap">`) {
		t.Fatal("an away session is offered for connecting")
	}
	form := url.Values{"session": {"lead"}, "machine": {"gpu-box"}, "remote": {"trainer"}, "permission": {"tasks-ask"}, "note": {"hi"}}
	wantContains(t, b.follow("/sessions", "/sessions/connect", form).body, "This needs your login password.")
	form.Set("password", "pw")
	wantContains(t, b.follow("/sessions", "/sessions/connect", form).body,
		"Link 5: asked gpu-box/trainer for a link from lead. The other side decides.")
	// The daemon's gate refused the first try before the handler ran.
	if got := fd.called(ipc.MethodLinkConnectAs); len(got) != 1 || got[0] != `{"session":"lead","target":"gpu-box/trainer","permission":"tasks-ask","note":"hi"}` {
		t.Fatalf("connect calls %v", got)
	}
}

// Disconnect and restrict need no password.
func TestDisconnectAndRestrict(t *testing.T) {
	fd := sessionsDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/sessions", "/sessions/restrict", url.Values{"link": {"1"}, "permission": {"messages"}}).body, "Link 1 now allows messages.")
	wantContains(t, b.follow("/sessions", "/sessions/disconnect", url.Values{"link": {"1"}}).body, "Disconnected link 1.")
	wantContains(t, b.follow("/sessions", "/sessions/disconnect", url.Values{"link": {"x"}}).body, "bad request: link must be a link number")
	if got := fd.called(ipc.MethodLinkDisconnect); len(got) != 1 || got[0] != `{"link":1}` {
		t.Fatalf("disconnect calls %v", got)
	}
}

// Every page still renders while the kill switch is on, with the daemon's
// refusal shown.
func TestSessionsPageWhileKilled(t *testing.T) {
	fd := sessionsDaemon(t)
	fd.setKilled(true)
	page := newUI(t, fd).open().get("/sessions").body
	wantContains(t, page, "<td>lead</td>", "The kill switch is on. Resume on the Status page first.")
	if strings.Contains(page, "gpu-box/trainer") {
		t.Fatal("links shown although the daemon refused them")
	}
}
```

Create `internal/webui/pages_approvals_test.go`:

```go
package webui

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

func approvalsDaemon(t *testing.T) *fakeDaemon {
	fd := sessionsDaemon(t)
	// Like the daemon: rejecting needs nothing, accepting needs the unlock.
	fd.handle(ipc.MethodLinkDecide, ipc.GateNone, func(cs *ipc.ConnState, p json.RawMessage) (any, error) {
		var dp ipc.LinkDecideParams
		json.Unmarshal(p, &dp)
		if dp.Accept && !cs.Unlocked() {
			return nil, core.ErrAuthRequired
		}
		return ipc.LinkView{Link: dp.Link, Machine: "mac", RemoteSession: "helper", PermissionIn: dp.Permission}, nil
	})
	full := "run the tests\n" + hostile + "\n" + strings.Repeat("x", 600)
	fd.reply(ipc.MethodApprovalsList, ipc.GateUnlock, ipc.ApprovalsListResult{Tasks: []ipc.ApprovalView{
		{TaskID: "T1", Peer: "gpu-box", Preview: full[:500], Full: full, SHA256: "abc123", Size: len(full), Received: time.Date(2026, 9, 26, 11, 0, 0, 0, time.UTC)},
	}})
	fd.reply(ipc.MethodApprovalsDecide, ipc.GateUnlock, nil)
	return fd
}

func TestLinkRequestsOnTheApprovalsPage(t *testing.T) {
	fd := approvalsDaemon(t)
	b := newUI(t, fd).open()
	page := b.get("/approvals").body
	wantContains(t, page, "Link 2: <strong>mac/helper</strong> asks to link with your session <strong>lead</strong> and asks for <strong>tasks-ask</strong>.",
		`<option value="tasks-ask" selected>tasks-ask</option>`, "note: please link")
	if strings.Contains(page, "Link 1:") {
		t.Fatal("an active link is shown as a request")
	}
	accept := url.Values{"link": {"2"}, "permission": {"messages"}}
	wantContains(t, b.follow("/approvals", "/approvals/link/accept", accept).body, "This needs your login password.")
	accept.Set("password", "pw")
	wantContains(t, b.follow("/approvals", "/approvals/link/accept", accept).body, "Accepted link 2: mac/helper may now use messages.")
	wantContains(t, b.follow("/approvals", "/approvals/link/reject", url.Values{"link": {"2"}}).body, "Rejected link 2.")
	got := fd.called(ipc.MethodLinkDecide)
	if len(got) != 3 || got[1] != `{"link":2,"accept":true,"permission":"messages"}` || got[2] != `{"link":2,"accept":false}` {
		t.Fatalf("decide calls %v", got)
	}
}

// Rejecting needs no password, also in a browser session that never
// unlocked.
func TestRejectNeedsNoPassword(t *testing.T) {
	fd := approvalsDaemon(t)
	b := newUI(t, fd).open()
	wantContains(t, b.follow("/approvals", "/approvals/link/reject", url.Values{"link": {"2"}}).body, "Rejected link 2.")
	if len(fd.called(ipc.MethodAuthUnlock)) != 0 {
		t.Fatal("rejecting asked for the password")
	}
}

// Tasks waiting for approval need the password to see and to decide.
func TestTasksNeedThePassword(t *testing.T) {
	fd := approvalsDaemon(t)
	b := newUI(t, fd).open()
	page := b.get("/approvals").body
	wantContains(t, page, "Enter your password to see tasks waiting for your approval.")
	if strings.Contains(page, "run the tests") {
		t.Fatal("task text shown before the password")
	}
	wantContains(t, b.follow("/approvals", "/approvals/unlock", nil).body, "This needs your login password.")
	page = b.follow("/approvals", "/approvals/unlock", url.Values{"password": {"pw"}}).body
	wantContains(t, page, "Task <code>T1</code> from <strong>gpu-box</strong>", "run the tests\n&lt;/pre&gt;&lt;script&gt;",
		"<summary>Full text</summary>", "SHA-256 <code>abc123</code>")
	wantContains(t, b.follow("/approvals", "/approvals/task", url.Values{"task_id": {"T1"}, "decision": {"approve"}}).body, "Approved task T1.")
	wantContains(t, b.follow("/approvals", "/approvals/task", url.Values{"task_id": {"T1"}, "decision": {"deny"}}).body, "Denied task T1.")
	got := fd.called(ipc.MethodApprovalsDecide)
	if len(got) != 2 || got[0] != `{"task_id":"T1","approve":true}` || got[1] != `{"task_id":"T1","approve":false}` {
		t.Fatalf("decide calls %v", got)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/webui -run '^(TestSessionsPageShowsLocalSessionsAndLinks|TestPeerStringsAreEscapedAndCleaned|TestConnectFromTheSessionsPage|TestDisconnectAndRestrict|TestSessionsPageWhileKilled|TestLinkRequestsOnTheApprovalsPage|TestRejectNeedsNoPassword|TestTasksNeedThePassword)$' -count=1
```

Expected: FAIL; all eight tests fail because `/sessions`, `/approvals` and their actions are not registered yet (the routes answer `404 page not found`).

- [ ] **Step 3: Implement**

Create `internal/webui/links.go`:

```go
package webui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// permissions are the link levels, lowest first.
var permissions = []core.Permission{core.PermMessages, core.PermTasksAsk, core.PermTasksAuto}

// linkRow is a link as the pages show it.
type linkRow struct {
	ipc.LinkView
	StateText string
	TheyMay   string
	Peer      string            // the peer's purpose and note, cleaned
	Lower     []core.Permission // levels the human may restrict to
}

func newLinkRow(l ipc.LinkView) linkRow {
	r := linkRow{LinkView: l, Peer: peerText(l.Wrapped)}
	switch {
	case l.State == "pending" && l.Direction == "in":
		r.StateText = "request (you decide)"
	case l.State == "pending":
		r.StateText = "requested (they decide)"
	case l.State == "closed":
		r.StateText = "closed: " + cleanLine(l.Reason)
	case l.RemoteAway:
		r.StateText = "active (peer away)"
	default:
		r.StateText = cleanLine(l.State)
	}
	r.TheyMay = dash(l.PermissionIn)
	if l.State == "pending" && l.Direction == "in" {
		r.TheyMay = "asks " + l.Proposed
	}
	if l.State == "active" {
		for _, p := range permissions {
			if p.Below(core.Permission(l.PermissionIn)) {
				r.Lower = append(r.Lower, p)
			}
		}
	}
	return r
}

// isRequest reports whether l is a link request waiting for this side.
func isRequest(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" }

// formLink reads the link number of a form.
func formLink(rq *Request) (int64, error) {
	n, err := strconv.ParseInt(rq.Form("link"), 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: link must be a link number", ipc.ErrBadRequest)
	}
	return n, nil
}

// peerLines cleans multi-line peer text (a task's instructions) line by line.
func peerLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = cleanLine(strings.ReplaceAll(l, "\t", "    "))
	}
	return strings.Join(lines, "\n")
}
```

Create `internal/webui/pages_sessions.go`:

```go
package webui

import (
	"context"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// addSessions is the Sessions page: local shared sessions, their links, and
// the sessions each paired device shows this machine. Disconnect and
// restrict need nothing; asking for a link on a session's behalf needs the
// password (link.connect_as).
func addSessions(r *Registry) {
	r.AddPage(Page{Path: "/sessions", Title: "Sessions", Template: "sessions.html", Load: loadSessions})
	r.AddAction(Action{Path: "/sessions/connect", Back: "/sessions", Run: connectAs})
	r.AddAction(Action{Path: "/sessions/disconnect", Back: "/sessions", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		if err := rq.Call(ctx, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: n}, nil); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Disconnected link %d.", n)}, nil
	}})
	r.AddAction(Action{Path: "/sessions/restrict", Back: "/sessions", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		var v ipc.LinkView
		if err := rq.Call(ctx, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: n, Permission: rq.Form("permission")}, &v); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Link %d now allows %s.", v.Link, v.PermissionIn)}, nil
	}})
}

// remoteRow is a session another device shows this machine.
type remoteRow struct {
	ipc.RemoteSessionView
	Purpose string
}

type sessionsData struct {
	Local       []ipc.SharedSessionView
	Open        []string // local sessions that can ask for a link
	Links       []linkRow
	Requests    int
	Peers       []ipc.PeerView
	Machine     string
	Remote      []remoteRow
	Permissions []core.Permission
}

func loadSessions(ctx context.Context, rq *Request) (any, error) {
	d := sessionsData{Machine: rq.Query("machine"), Permissions: permissions}
	var local ipc.LocalSessionsResult
	if err := rq.Call(ctx, ipc.MethodSessionsLocal, nil, &local); err != nil {
		return nil, err
	}
	d.Local = local.Sessions
	for _, s := range local.Sessions {
		if s.State == string(core.SessionOpen) {
			d.Open = append(d.Open, s.Name)
		}
	}
	var links ipc.LinksResult
	if err := rq.Call(ctx, ipc.MethodLinks, nil, &links); err != nil {
		return d, err
	}
	for _, l := range links.Links {
		if isRequest(l) {
			d.Requests++
			continue
		}
		d.Links = append(d.Links, newLinkRow(l))
	}
	var peers ipc.PeerListResult
	if err := rq.Call(ctx, ipc.MethodMachines, nil, &peers); err != nil {
		return d, err
	}
	d.Peers = peers.Peers
	if d.Machine == "" {
		return d, nil
	}
	var remote ipc.SessionsListResult
	if err := rq.Call(ctx, ipc.MethodSessionsList, ipc.MachineParams{Machine: d.Machine}, &remote); err != nil {
		return d, err
	}
	d.Machine = remote.Machine
	for _, s := range remote.Sessions {
		d.Remote = append(d.Remote, remoteRow{RemoteSessionView: s, Purpose: peerText(s.Wrapped)})
	}
	return d, nil
}

func connectAs(ctx context.Context, rq *Request) (Reply, error) {
	session, machine, remote := rq.Form("session"), rq.Form("machine"), rq.Form("remote")
	target := machine + "/" + remote
	var v ipc.LinkView
	if err := rq.WithPassword(ctx, func() error {
		return rq.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{
			Session: session, Target: target, Permission: rq.Form("permission"), Note: rq.Form("note"),
		}, &v)
	}); err != nil {
		return Reply{}, err
	}
	return Reply{Notice: fmt.Sprintf("Link %d: asked %s for a link from %s. The other side decides.", v.Link, target, session)}, nil
}
```

Create `internal/webui/pages_approvals.go`:

```go
package webui

import (
	"context"
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// addApprovals is the Approvals page: link requests waiting for this side
// and tasks under tasks-ask links. Rejecting a link needs nothing;
// accepting one, and seeing or deciding tasks, need the password.
func addApprovals(r *Registry) {
	r.AddPage(Page{Path: "/approvals", Title: "Approvals", Template: "approvals.html", Load: loadApprovals})
	r.AddAction(Action{Path: "/approvals/link/accept", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		var v ipc.LinkView
		if err := rq.WithPassword(ctx, func() error {
			return rq.Call(ctx, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: n, Accept: true, Permission: rq.Form("permission")}, &v)
		}); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Accepted link %d: %s/%s may now use %s.", v.Link, v.Machine, v.RemoteSession, v.PermissionIn)}, nil
	}})
	r.AddAction(Action{Path: "/approvals/link/reject", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		n, err := formLink(rq)
		if err != nil {
			return Reply{}, err
		}
		if err := rq.Call(ctx, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: n}, nil); err != nil {
			return Reply{}, err
		}
		return Reply{Notice: fmt.Sprintf("Rejected link %d.", n)}, nil
	}})
	r.AddAction(Action{Path: "/approvals/unlock", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		if rq.HTTP.PostFormValue("password") == "" {
			return Reply{}, core.ErrAuthRequired
		}
		if err := rq.WithPassword(ctx, func() error { return nil }); err != nil {
			return Reply{}, err
		}
		return Reply{}, nil
	}})
	r.AddAction(Action{Path: "/approvals/task", Back: "/approvals", Run: func(ctx context.Context, rq *Request) (Reply, error) {
		id, approve := rq.Form("task_id"), rq.Form("decision") == "approve"
		if err := rq.WithPassword(ctx, func() error {
			return rq.Call(ctx, ipc.MethodApprovalsDecide, ipc.ApprovalsDecideParams{TaskID: id, Approve: approve}, nil)
		}); err != nil {
			return Reply{}, err
		}
		if approve {
			return Reply{Notice: "Approved task " + id + "."}, nil
		}
		return Reply{Notice: "Denied task " + id + "."}, nil
	}})
}

// taskRow is a task waiting for approval. Preview and Full are the peer's
// instructions, cleaned line by line.
type taskRow struct {
	ipc.ApprovalView
	PreviewText string
	FullText    string
	Truncated   bool
}

type approvalsData struct {
	Requests    []linkRow
	Tasks       []taskRow
	TasksLocked bool
	Permissions []core.Permission
}

func loadApprovals(ctx context.Context, rq *Request) (any, error) {
	d := approvalsData{Permissions: permissions}
	var links ipc.LinksResult
	if err := rq.Call(ctx, ipc.MethodLinks, nil, &links); err != nil {
		return nil, err
	}
	for _, l := range links.Links {
		if isRequest(l) {
			d.Requests = append(d.Requests, newLinkRow(l))
		}
	}
	var tasks ipc.ApprovalsListResult
	err := rq.Call(ctx, ipc.MethodApprovalsList, nil, &tasks)
	if errors.Is(err, core.ErrAuthRequired) {
		d.TasksLocked = true
		return d, nil
	}
	if err != nil {
		return d, err
	}
	for _, t := range tasks.Tasks {
		d.Tasks = append(d.Tasks, taskRow{ApprovalView: t, PreviewText: peerLines(t.Preview), FullText: peerLines(t.Full), Truncated: t.Preview != t.Full})
	}
	return d, nil
}
```

Create `internal/webui/assets/templates/sessions.html`:

```html
{{define "content"}}{{$csrf := .CSRF}}{{with .Data}}{{$d := .}}
<section>
<h2>Shared sessions on this machine</h2>
{{if .Local}}
<div class="table-wrap"><table>
<thead><tr><th>Session</th><th>Purpose</th><th>Visible to</th><th>State</th><th>Agent</th></tr></thead>
<tbody>
{{range .Local}}<tr><td>{{.Name}}</td><td>{{dash (clean .Purpose)}}</td><td>{{.Visibility}}</td><td>{{.State}}</td><td>{{dash .Agent}}</td></tr>
{{end}}
</tbody>
</table></div>
{{else}}<p class="muted">No shared sessions. An agent chat shares one with its session_share tool.</p>{{end}}
</section>
<section>
<h2>Links</h2>
{{if .Requests}}<p>Link requests waiting for you: {{.Requests}}. <a href="/approvals">Decide them on the Approvals page.</a></p>{{end}}
{{if .Links}}
<div class="table-wrap"><table>
<thead><tr><th>Link</th><th>Session</th><th>Other side</th><th>State</th><th>They may</th><th>You may</th><th></th></tr></thead>
<tbody>
{{range .Links}}<tr>
<td>{{.Link}}</td><td>{{dash .Session}}</td><td>{{.Machine}}/{{.RemoteSession}}{{if .Peer}}<pre class="peer">{{.Peer}}</pre>{{end}}</td>
<td>{{.StateText}}</td><td>{{.TheyMay}}</td><td>{{dash .PermissionOut}}</td>
<td>{{if ne .State "closed"}}
{{if .Lower}}<form method="post" action="/sessions/restrict"><input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="link" value="{{.Link}}">
<select name="permission">{{range .Lower}}<option value="{{.}}">{{.}}</option>{{end}}</select><button type="submit" class="secondary">Restrict</button></form>{{end}}
<form method="post" action="/sessions/disconnect" data-confirm="Disconnect link {{.Link}}? It closes for good on both sides."><input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="link" value="{{.Link}}"><button type="submit" class="danger">Disconnect</button></form>
{{end}}</td>
</tr>{{end}}
</tbody>
</table></div>
{{else}}<p class="muted">No links.</p>{{end}}
</section>
<section>
<h2>Sessions on other devices</h2>
{{if .Peers}}<p>{{range .Peers}}<a href="/sessions?machine={{.Alias}}">Show sessions on {{.Alias}}</a> {{end}}</p>{{else}}<p class="muted">No paired devices. Pair one on the Devices page.</p>{{end}}
{{if .Machine}}
<h2>{{.Machine}}</h2>
{{if .Remote}}
<div class="table-wrap"><table>
<thead><tr><th>Session</th><th>State</th><th>Agent</th><th>Ask for a link</th></tr></thead>
<tbody>
{{range .Remote}}<tr>
<td>{{.Name}}{{if .Purpose}}<pre class="peer">{{.Purpose}}</pre>{{end}}</td><td>{{.State}}</td><td>{{dash .Agent}}</td>
<td>{{if $d.Open}}<form method="post" action="/sessions/connect">
<input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="machine" value="{{$d.Machine}}"><input type="hidden" name="remote" value="{{.Name}}">
<label>From <select name="session">{{range $d.Open}}<option value="{{.}}">{{.}}</option>{{end}}</select></label>
<label>To do there <select name="permission">{{range $d.Permissions}}<option value="{{.}}">{{.}}</option>{{end}}</select></label>
<input name="note" maxlength="280" placeholder="Note (optional)">
{{template "password" (not $.Unlocked)}}
<button type="submit">Connect</button>
</form>{{else}}<span class="muted">Share a session here first.</span>{{end}}</td>
</tr>{{end}}
</tbody>
</table></div>
{{else}}<p class="muted">{{.Machine}} shows you no sessions.</p>{{end}}
{{end}}
</section>
{{end}}{{end}}
```

Create `internal/webui/assets/templates/approvals.html`:

```html
{{define "content"}}{{$csrf := .CSRF}}{{with .Data}}{{$d := .}}
<section>
<h2>Link requests</h2>
{{if .Requests}}
{{range .Requests}}<div>
<p>Link {{.Link}}: <strong>{{.Machine}}/{{.RemoteSession}}</strong> asks to link with your session <strong>{{dash .Session}}</strong> and asks for <strong>{{.Proposed}}</strong>.</p>
{{if .Peer}}<p class="muted">From the other machine:</p><pre class="peer">{{.Peer}}</pre>{{end}}
<form method="post" action="/approvals/link/accept">
<input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="link" value="{{.Link}}">
<label>Grant <select name="permission">{{$p := .Proposed}}{{range $d.Permissions}}<option value="{{.}}"{{if eq (print .) $p}} selected{{end}}>{{.}}</option>{{end}}</select></label>
{{template "password" (not $.Unlocked)}}
<button type="submit">Accept</button>
</form>
<form method="post" action="/approvals/link/reject">
<input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="link" value="{{.Link}}">
<button type="submit" class="secondary">Reject</button>
</form>
</div>{{end}}
{{else}}<p class="muted">No link requests.</p>{{end}}
</section>
<section>
<h2>Tasks waiting for approval</h2>
{{if .TasksLocked}}
<p>Enter your password to see tasks waiting for your approval.</p>
<form method="post" action="/approvals/unlock" class="block">
<input type="hidden" name="csrf" value="{{$csrf}}">
{{template "password" true}}
<button type="submit">Show tasks</button>
</form>
{{else if .Tasks}}
{{range .Tasks}}<div>
<p>Task <code>{{.TaskID}}</code> from <strong>{{.Peer}}</strong>, received {{time .Received}}. {{.Size}} bytes, SHA-256 <code>{{.SHA256}}</code>.</p>
<pre class="peer">{{.PreviewText}}</pre>
{{if .Truncated}}<details><summary>Full text</summary><pre class="peer">{{.FullText}}</pre></details>{{end}}
<form method="post" action="/approvals/task">
<input type="hidden" name="csrf" value="{{$csrf}}"><input type="hidden" name="task_id" value="{{.TaskID}}">
{{template "password" (not $.Unlocked)}}
<button type="submit" name="decision" value="approve">Approve</button>
<button type="submit" name="decision" value="deny" class="secondary">Deny</button>
</form>
</div>{{end}}
{{else}}<p class="muted">No tasks waiting.</p>{{end}}
</section>
{{end}}{{end}}
```

In `internal/webui/registry.go`, `DefaultRegistry`, replace the list:

```go
	for _, add := range []func(*Registry){
		addDevices,
		addActivity,
		addStatus,
	} {
```

with:

```go
	for _, add := range []func(*Registry){
		addDevices,
		addSessions,
		addApprovals,
		addActivity,
		addStatus,
	} {
```

- [ ] **Step 4: Run the tests to see them pass**

```bash
go test ./internal/webui -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: every `internal/webui` test PASSES, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/webui
git commit -m "webui: Sessions page with connect, disconnect and restrict, and the Approvals page

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: ui: cravv-connect ui opens the web UI; the daemon serves it with every IPC gate

`app.Serve` (used by `cravv-connect daemon run` and by the e2e harness) now builds the web UI: a `webui.Launcher` whose browser sessions are `srv.Pipe` connections into the same IPC server, registers the UI methods with the launcher as `ui.start`'s port, and on the way out stops the UI and waits until no UI connection is still being served, so the daemon is closed after it. `cravv-connect ui` calls `ui.start`, prints the link and opens the browser.

**Files:**
- Create: `internal/cli/cmd_ui.go`
- Modify: `internal/app/run.go`, `internal/app/ui.go`
- Test: `internal/cli/ui_test.go` (new), `internal/app/ui_test.go`, `e2e/webui_test.go` (new)

**Interfaces:**

Consumes: Tasks 1 to 6; the cli test harness (`newFakeDaemon`, `reply`, `start`, `run`); the e2e harness (`NewPair`, `Share`, `Conn`, `Call`, `WaitLink`, `Link`, `AllLinks`, `Status`, `Password`).

Produces:

```go
// internal/app/ui.go (unexported)
type webUI struct{ ... }
func newWebUI(ctx context.Context, srv *ipc.Server, clock core.Clock, logger *slog.Logger) *webUI
// internal/cli/cmd_ui.go
// cravv-connect ui [--no-browser]
// e2e/webui_test.go
type UIBrowser struct{ ... }
func OpenUI(t *testing.T, n *Node) *UIBrowser
func (b *UIBrowser) Get(path string) string
func (b *UIBrowser) Submit(page, action string, form url.Values) string
```

**Design notes:**
- `webUI.dial` refuses after `close` began, and counts each pipe until its `ServeConn` returns; `close` stops the launcher (which closes every browser session's client) and waits. `Serve` cancels its context before the deferred `close` runs, so pipes end either way.
- The browser is opened with `open` (macOS) or `xdg-open` (elsewhere) and not waited for; a failure is reported on stderr and the command still succeeds, since the link is printed.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/ui_test.go`:

```go
package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/ipc"
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
```

Replace the whole content of `internal/app/ui_test.go` with (Task 3's test plus `TestServeStopsTheWebUI`):

```go
package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/auth"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/store"
)

// uiDaemon serves a real daemon with the UI methods over in-process pipes.
func uiDaemon(t *testing.T) (dir string, dial func() *ipc.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "appui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := config.Paths{
		Home: dir, Config: filepath.Join(dir, "config.toml"), DB: filepath.Join(dir, "store.db"),
		Audit: filepath.Join(dir, "audit.log"), Socket: filepath.Join(dir, "d.sock"),
		Files: filepath.Join(dir, "files"), Log: filepath.Join(dir, "daemon.log"),
	}
	d, err := daemon.New(daemon.Options{
		Paths: paths, Config: config.Config{DeviceName: "test-mac"}, Verifier: auth.Fake{Password: "pw"}, Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore { return daemon.SettingsIdentityStore{Settings: s} },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	srv := api.NewServer(Ports(d), core.SystemClock{}, nil)
	api.RegisterUI(srv, UIPorts(d, nil))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return dir, func() *ipc.Client {
		c, _ := srv.Pipe(ctx)
		t.Cleanup(func() { c.Close() })
		return c
	}
}

func TestUIPortsAgainstRealDaemon(t *testing.T) {
	dir, dial := uiDaemon(t)
	ctx := context.Background()
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(proj, 0o700)
	share := func(name, vis string) *ipc.Client {
		c := dial()
		if err := c.Call(ctx, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: proj}, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.Call(ctx, ipc.MethodSessionShare, ipc.SessionShareParams{Name: name, Purpose: name + " work", Visibility: vis}, nil); err != nil {
			t.Fatal(err)
		}
		return c
	}
	share("lead", "all-peers")
	share("gone", "private").Call(ctx, ipc.MethodSessionClose, nil, nil)

	human := dial()
	var r ipc.LocalSessionsResult
	if err := human.Call(ctx, ipc.MethodSessionsLocal, nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Sessions) != 1 || r.Sessions[0].Name != "lead" || r.Sessions[0].Visibility != "all-peers" || r.Sessions[0].Purpose != "lead work" {
		t.Fatalf("local sessions %+v", r.Sessions)
	}
	if err := human.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "pw"}, nil); err != nil {
		t.Fatal(err)
	}
	err := human.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "gone", Target: "bob/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), `no open session "gone"`) {
		t.Fatalf("closed session: %v", err)
	}
	// An open session gets as far as the target machine, which is not paired.
	err = human.Call(ctx, ipc.MethodLinkConnectAs, ipc.LinkConnectAsParams{Session: "lead", Target: "bob/trainer", Permission: "messages"}, nil)
	if !errors.Is(err, core.ErrNotFound) || !strings.Contains(err.Error(), `peer "bob"`) {
		t.Fatalf("open session: %v", err)
	}
}

// Serve starts the web UI on ui.start and stops it before it returns.
func TestServeStopsTheWebUI(t *testing.T) {
	dir, err := os.MkdirTemp("", "serveui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	paths := config.Paths{
		Home: dir, Config: filepath.Join(dir, "config.toml"), DB: filepath.Join(dir, "store.db"),
		Audit: filepath.Join(dir, "audit.log"), Socket: filepath.Join(dir, "d.sock"),
		Files: filepath.Join(dir, "files"), Log: filepath.Join(dir, "daemon.log"),
	}
	d, err := daemon.New(daemon.Options{
		Paths: paths, Config: config.Config{DeviceName: "serve-ui"}, Verifier: auth.Fake{Password: "pw"}, Username: "tester",
		IdentityStore: func(s store.SettingsStore) daemon.IdentityStore { return daemon.SettingsIdentityStore{Settings: s} },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ln, err := ipc.Listen(paths.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, d, ln, core.SystemClock{}, nil) }()

	var r ipc.UIStartResult
	if err := dial(t, paths.Socket).Call(ctx, ipc.MethodUIStart, nil, &r); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(r.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || len(res.Cookies()) != 1 {
		t.Fatalf("launch: %d, cookies %v", res.StatusCode, res.Cookies())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	if c, err := net.DialTimeout("tcp", u.Host, time.Second); err == nil {
		c.Close()
		t.Fatal("the web UI still accepts connections after Serve returned")
	}
}
```

Create `e2e/webui_test.go`:

```go
package e2e

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/ipc"
)

// UIBrowser is a browser session on a node's web UI: it keeps the session
// cookie and submits forms with the page's CSRF token and the UI's origin.
type UIBrowser struct {
	t      *testing.T
	base   string
	client *http.Client
}

// OpenUI asks the node's daemon to start the UI (as `cravv-connect ui`
// does) and follows the launch link.
func OpenUI(t *testing.T, n *Node) *UIBrowser {
	t.Helper()
	var r ipc.UIStartResult
	Call(t, n.Conn(), ipc.MethodUIStart, nil, &r)
	u, err := url.Parse(r.URL)
	if err != nil || u.Hostname() != "127.0.0.1" {
		t.Fatalf("launch URL %q", r.URL)
	}
	jar, _ := cookiejar.New(nil)
	b := &UIBrowser{t: t, base: "http://" + u.Host, client: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	code, loc, _ := b.send("GET", r.URL, nil)
	if code != http.StatusSeeOther || loc != "/" {
		t.Fatalf("launch: %d to %q", code, loc)
	}
	return b
}

func (b *UIBrowser) send(method, target string, form url.Values) (code int, location, body string) {
	b.t.Helper()
	if strings.HasPrefix(target, "/") {
		target = b.base + target
	}
	var rd io.Reader
	if form != nil {
		rd = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, target, rd)
	if err != nil {
		b.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", b.base)
	}
	res, err := b.client.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header.Get("Location"), string(raw)
}

// Get loads a page and fails the test unless it renders.
func (b *UIBrowser) Get(path string) string {
	b.t.Helper()
	code, _, body := b.send("GET", path, nil)
	if code != http.StatusOK {
		b.t.Fatalf("GET %s: %d %s", path, code, body)
	}
	return body
}

var uiCSRF = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

// Submit posts a form from page and returns the page the action redirected
// to (with its notices).
func (b *UIBrowser) Submit(page, action string, form url.Values) string {
	b.t.Helper()
	m := uiCSRF.FindStringSubmatch(b.Get(page))
	if m == nil {
		b.t.Fatalf("no form token on %s", page)
	}
	form.Set("csrf", m[1])
	code, loc, body := b.send("POST", action, form)
	if code != http.StatusSeeOther {
		b.t.Fatalf("POST %s: %d %s", action, code, body)
	}
	return b.Get(loc)
}

func wantPage(t *testing.T, page string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(page, p) {
			t.Fatalf("page lacks %q:\n%s", p, page)
		}
	}
}

// Two humans link two sessions from their web UIs alone: alice asks
// bob/trainer for a link from lead (with her password), bob accepts at a
// lower level (with his), and both daemons agree the link is active. The
// UI goes through the real IPC gates: without the password the request is
// refused.
func TestWebUIConnectAndAccept(t *testing.T) {
	t.Parallel()
	_, a, b := NewPair(t)
	a.Share("claude", "lead", "private")
	b.Share("claude", "trainer", "all-peers")
	ua, ub := OpenUI(t, a), OpenUI(t, b)

	wantPage(t, ua.Get("/sessions"), "<td>lead</td>", `href="/sessions?machine=bob"`)
	wantPage(t, ua.Get("/sessions?machine=bob"), "<h2>bob</h2>", `name="remote" value="trainer"`, "purpose: trainer work")

	form := url.Values{"session": {"lead"}, "machine": {"bob"}, "remote": {"trainer"}, "permission": {"tasks-ask"}, "note": {"from the web UI"}}
	wantPage(t, ua.Submit("/sessions?machine=bob", "/sessions/connect", form), "This needs your login password.")
	if len(a.AllLinks()) != 0 {
		t.Fatal("a link was requested without the password")
	}
	form.Set("password", Password)
	wantPage(t, ua.Submit("/sessions?machine=bob", "/sessions/connect", form), "asked bob/trainer for a link from lead. The other side decides.")

	in := b.WaitLink(10*time.Second, "the request arrives", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	num := strconv.FormatInt(in.Link, 10)
	wantPage(t, ub.Get("/approvals"), "<strong>alice/lead</strong> asks to link with your session <strong>trainer</strong> and asks for <strong>tasks-ask</strong>.",
		"note: from the web UI")
	accept := url.Values{"link": {num}, "permission": {"messages"}}
	wantPage(t, ub.Submit("/approvals", "/approvals/link/accept", accept), "This needs your login password.")
	accept.Set("password", Password)
	wantPage(t, ub.Submit("/approvals", "/approvals/link/accept", accept), "Accepted link "+num+": alice/lead may now use messages.",
		"Password unlocked until")

	a.WaitLink(10*time.Second, "alice sees the link accepted", func(l ipc.LinkView) bool {
		return l.State == "active" && l.RemoteSession == "trainer" && l.PermissionOut == "messages"
	})
	if l := b.Link(in.Link); l.State != "active" || l.PermissionIn != "messages" {
		t.Fatalf("bob's link %+v", l)
	}
	wantPage(t, ua.Get("/sessions"), "bob/trainer", "<td>active</td>")

	// Disconnecting from bob's page closes the link on both sides.
	wantPage(t, ub.Submit("/sessions", "/sessions/disconnect", url.Values{"link": {num}}), "Disconnected link "+num+".")
	a.WaitLink(10*time.Second, "alice sees the link closed", func(l ipc.LinkView) bool { return l.State == "closed" })
}

// The kill switch from the page, and resuming with the password.
func TestWebUIKillAndResume(t *testing.T) {
	t.Parallel()
	_, a, _ := NewPair(t)
	ua := OpenUI(t, a)
	wantPage(t, ua.Submit("/status", "/status/kill", url.Values{}), "The kill switch is on. Every link is closed.")
	if !a.Status().Killed {
		t.Fatal("not killed")
	}
	wantPage(t, ua.Get("/sessions"), "The kill switch is on. Resume on the Status page first.")
	wantPage(t, ua.Submit("/status", "/status/resume", url.Values{"password": {"wrong"}}), "Incorrect password.")
	wantPage(t, ua.Submit("/status", "/status/resume", url.Values{"password": {Password}}), "Resumed.")
	if a.Status().Killed {
		t.Fatal("still killed")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/cli -run '^(TestUIOpensTheBrowser|TestUINoBrowser|TestUIBrowserFailureStillPrintsTheLink)$' -count=1
go test ./internal/app -run '^TestServeStopsTheWebUI$' -count=1
go test ./e2e -run '^(TestWebUIConnectAndAccept|TestWebUIKillAndResume)$' -count=1
```

Expected: all FAIL. The cli tests fail to compile:

```
internal/cli/ui_test.go:15:9: undefined: openBrowser
```

and the app and e2e tests fail at the first `ui.start`:

```
unknown method "ui.start"
```

- [ ] **Step 3: Implement**

Replace the whole content of `internal/app/ui.go` with:

```go
package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/cravv/cravv-connect/internal/api"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/webui"
)

// UIPorts adapts the daemon to the web UI methods. ui starts the web server
// (Serve passes the webui.Launcher); link requests reuse the links adapter.
func UIPorts(d *daemon.Daemon, ui api.UIPort) api.UIPorts {
	return api.UIPorts{UI: ui, Local: localSessions{d}, Links: links{d}}
}

// localSessions adapts the daemon's SessionService to api.LocalSessionPort.
type localSessions struct{ d *daemon.Daemon }

func (a localSessions) Local(ctx context.Context) ([]ipc.SharedSessionView, error) {
	list, err := a.d.Shared().List(ctx, core.SessionOpen, core.SessionAway)
	if err != nil {
		return nil, err
	}
	out := make([]ipc.SharedSessionView, 0, len(list))
	for _, s := range list {
		out = append(out, shared{a.d}.view(ctx, s))
	}
	return out, nil
}

func (a localSessions) OpenByName(ctx context.Context, name string) (string, error) {
	list, err := a.d.Shared().List(ctx, core.SessionOpen)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Name == name {
			return s.ID, nil
		}
	}
	return "", fmt.Errorf("no open session %q on this machine: %w", name, core.ErrNotFound)
}

// webUI is the daemon's web UI: a webui.Launcher whose browser sessions are
// in-process pipes into the IPC server, so every UI action passes the same
// gates as the CLI.
type webUI struct {
	launcher *webui.Launcher
	srv      *ipc.Server
	ctx      context.Context
	mu       sync.Mutex
	closed   bool
	pipes    sync.WaitGroup
}

// newWebUI builds the UI for srv. It stops when ctx ends.
func newWebUI(ctx context.Context, srv *ipc.Server, clock core.Clock, logger *slog.Logger) *webUI {
	w := &webUI{srv: srv, ctx: ctx}
	w.launcher = webui.NewLauncher(ctx, webui.Options{Clock: clock, Dial: w.dial, Logger: logger})
	return w
}

// dial opens a browser session's connection to the IPC server.
func (w *webUI) dial() (webui.Caller, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, webui.ErrStopping
	}
	c, done := w.srv.Pipe(w.ctx)
	w.pipes.Add(1)
	go func() {
		<-done
		w.pipes.Done()
	}()
	return c, nil
}

// close stops the UI server and waits until no browser session's
// connection is being served, so the daemon can be closed after it.
func (w *webUI) close() {
	w.launcher.Stop()
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.pipes.Wait()
}
```

In `internal/app/run.go`, `Serve`, after `srv := api.NewServer(ports, clock, logger)`, add:

```go
	// The web UI starts on ui.start and is stopped before Serve returns.
	ui := newWebUI(ctx, srv, clock, logger)
	defer ui.close()
	api.RegisterUI(srv, UIPorts(d, ui.launcher))
```

Create `internal/cli/cmd_ui.go`:

```go
package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/webui"
	"github.com/spf13/cobra"
)

func init() { Register(newUICmd) }

// openBrowser opens url in the default browser. Tests replace it.
var openBrowser = func(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, url)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func newUICmd(env *Env) *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the local web UI in your browser",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			return withConn(ctx, env, func(c Caller) error {
				var r ipc.UIStartResult
				if err := c.Call(ctx, ipc.MethodUIStart, nil, &r); err != nil {
					return err
				}
				w := env.Stdout
				fmt.Fprintln(w, "cravv-connect UI:")
				fmt.Fprintf(w, "  %s\n", terminalSafe(r.URL))
				fmt.Fprintf(w, "The link works once, within %d minutes. Run cravv-connect ui again for a new one.\n", int(webui.LaunchTokenTTL.Minutes()))
				if noBrowser {
					return nil
				}
				if err := openBrowser(r.URL); err != nil {
					fmt.Fprintf(env.Stderr, "Could not open a browser (%s). Open the link above yourself.\n", terminalSafe(err.Error()))
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the link without opening a browser")
	return cmd
}
```

- [ ] **Step 4: Run the tests to see them pass**

```bash
go test ./internal/cli -run '^(TestUIOpensTheBrowser|TestUINoBrowser|TestUIBrowserFailureStillPrintsTheLink)$' -count=1 -race
go test ./internal/app -run '^(TestServeStopsTheWebUI|TestServeRunsDaemonAndAPIUntilCancelled)$' -count=1 -race
go test ./e2e -run '^(TestWebUIConnectAndAccept|TestWebUIKillAndResume)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
CGO_ENABLED=0 go build ./...
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, every package reports `ok` (e2e included), and the cgo-free build succeeds.

- [ ] **Step 5: Commit**

```bash
git add e2e/webui_test.go internal/app/run.go internal/app/ui.go internal/app/ui_test.go internal/cli/cmd_ui.go internal/cli/ui_test.go
git commit -m "ui: cravv-connect ui opens the web UI; the daemon serves it with every IPC gate

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Manual check after Task 7

With a daemon running (`cravv-connect daemon start`):

```bash
cravv-connect ui
```

Expected: it prints `cravv-connect UI:`, the `http://127.0.0.1:<port>/launch?token=...` link and `The link works once, within 2 minutes. Run cravv-connect ui again for a new one.`, and the browser opens on Devices with the address bar showing no token. Opening the launch link again shows `This link was already used or has expired. Run cravv-connect ui again for a new one.`, and the browser's network panel shows no request to any other host.
