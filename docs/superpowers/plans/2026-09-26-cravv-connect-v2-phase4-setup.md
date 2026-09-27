# cravv-connect v2 Phase 4: Simple Setup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A fresh machine goes from nothing to paired and chat-connected with an install one-liner plus one command, with no Go toolchain: `curl ... | sh`, then `cravv-connect setup` on the first machine (relay or a LAN test relay, admin token, init, the daemon as a login service, the agent integrations, and "pair a device now" with a join code and its QR code), and `cravv-connect setup --join <code>` on every other machine (confirm the relay in the code, then init, daemon, join, alias and agents). Tagged releases build `cravv-connect` for four platforms with cgo, plus `cravv-relay`, with checksums.

**Architecture:**
- `internal/relayaddr` (new) holds the relay URL rule for addresses that come from another machine (https, or plain http only for this machine or a private network). The daemon's `validRelayURL` and `setup --join` both use it.
- `internal/joincode` (new) builds and parses `cravv-join:<base32(relay origin)>:<nameplate>-<secret>` and turns a join code into the bind code for this machine's relay.
- `internal/qrcode` (new) draws a QR code in the terminal with Unicode half blocks; `rsc.io/qr` (pure Go, BSD-3-Clause) encodes it.
- `internal/cli` gains `setup` (`cmd_setup.go`, `setup_join.go`, `setup_lanrelay.go`, `setup_system.go`) and `version` (`cmd_version.go`); `pair` shows join codes and `join` accepts them (`joincode.go`). The wizard talks to the person only through the existing `Prompter`, to the network and process table only through the new `SetupSystem` interface (`Env.Setup`), to the daemon over IPC, to the login service through `daemon install`/`daemon stop`, and to agents only through the `install.Installer` interface.
- `.github/workflows/release.yml` and `ci.yml` build, test and publish; `scripts/install.sh` installs a release and is tested by a Go `httptest` harness in `scripts/`.
- The README's quick start is rewritten around the one-liner and `setup`.

**Tech Stack:** Go 1.26, `github.com/spf13/cobra`, `rsc.io/qr` v0.2.0 (new), POSIX `sh`, GitHub Actions (`actions/checkout@v4`, `actions/setup-go@v5`, `actions/setup-node@v4`, `actions/upload-artifact@v4`, `actions/download-artifact@v4`, `gh`).

**Spec:** `docs/superpowers/specs/2026-09-26-cravv-connect-v2-sessions-design.md` section 8 (setup), with 3.1 (machines and pairing) and 10 (pairing stays behind the password; relay rule). Phase 1 plan: `docs/superpowers/plans/2026-09-26-cravv-connect-v2-phase1-links.md`.

**Verified:** every task below was implemented test-first in a scratch worktree on top of `main` at `e8eedac`, one commit per task (Tasks 1 and 2 were committed first by an earlier session and are reproduced here). After each commit `gofmt -l internal e2e cmd scripts` (on the directories that exist) printed nothing, `go vet ./...` was clean and `go test ./... -race -count=1` passed (35 packages, e2e included); `CGO_ENABLED=0 go build ./...` passes at the end. The code blocks are those commits, byte for byte. The result also merges cleanly with `main` at `5a5c0a2` (Phases 2 and 5), where `internal/cli`, `scripts`, `cmd/...`, `internal/daemon` and the new packages pass.

## Global Constraints

- Everything in the Phase 1 plan's Global Constraints still holds (module path, cgo only in `internal/auth`, `crypto/rand` only, no em dashes in user-facing text).
- After every task: `gofmt -l internal e2e cmd` (plus `scripts` from Task 7 on) prints nothing, `go vet ./...` is clean, `go test ./... -race -count=1` passes (e2e included).
- **Merge safety (Phase 2 and the agent installers are changed in parallel).** Do not edit `internal/install/claude.go`, `internal/install/codex.go` or `internal/mcpserver`. Setup uses agents only through `install.Registry` and `install.Installer`. The only edits to existing files are: `internal/daemon/relayurl.go` (delegates to `relayaddr`), `internal/cli/cmd_pair.go` (join codes, `pairNow`, `joinNow`), `internal/cli/cmd_init.go` (the "Next:" line moves to `RunE`), `internal/cli/env.go` (one field, `Setup`), `go.mod`/`go.sum` (`rsc.io/qr`), `Makefile` (version stamp) and `README.md`.
- **The person decides.** Every change to this machine is either a flag the person typed or an answer to a question: the relay (or starting a LAN relay), the admin token, reset, the join code's relay, each agent, pairing. `--yes` answers the yes/no questions but never starts a LAN relay and never pairs.
- **Passwords only through `Prompter`.** The login password (join, pair) and the admin token are read with `Prompter.Password` (no echo, `/dev/tty`); an admin token setup generates travels only in the relay's environment, never on a command line.
- **Relay rule.** A relay URL that comes from another machine (a join code) must pass `relayaddr.Check`; a machine set up for one relay is never moved to another without `--reset`.
- **Time:** the wizard's waits (`setupOnlineWait` 30 seconds, `relayStartWait` 5 seconds) use wall-clock polling like the existing `daemon start`/`daemon install` waits; tests shorten the package variables.
- **Copy:** no em dashes in CLI text, the script, workflows or the README (`TestReadmeQuickStart` checks the README).

## Decisions

- **The version variable is the existing `cli.Version`.** It was already declared (and reported by the MCP server) with the documented `-X github.com/cookwithcravv/cravv-connect/internal/cli.Version=...`; a second `version` variable would drift. `cravv-connect version` falls back to the module version for `go install ...@vX.Y.Z` builds.
- **Archive names drop the tag's `v`:** tag `v1.2.0` gives `cravv-connect_1.2.0_darwin_arm64.tar.gz`, and `cravv-connect version` prints `v1.2.0`.
- **Setup refuses a public plain-http relay URL** even when typed by the person, because every joining machine would refuse its join codes; `init` still accepts it for special cases.
- **The LAN test relay is not a service:** it runs in memory until the machine restarts (documented), on the fixed port 8787, listening on every interface.
- **`--yes` also answers the `--reset` and "Join relay ...?" questions**, like `unpair --yes`; without `--yes` both default to no.
- **Agents already set up:** the installers cannot report that today, so setup offers each detected agent every run (installs are idempotent) and honours an optional `Installed() bool` if an installer adds one later.
- **Intel macOS runner.** The matrix uses `macos-13` as specified. GitHub announced that image's retirement; if it is no longer offered, switch that entry to `macos-15-intel` (one matrix line in `release.yml` and the matching line in `TestReleaseWorkflowShape`).
- **Agent options.** Setup calls the plain `Installer.Install`, so on `main` (where Claude Code's installer also takes `Options`) it installs with the defaults, without `--allow-send`; `cravv-connect install claude --allow-send` stays the way to widen it.
- **Not in this phase:** the web UI's Devices page still shows the plain bind code (the Phase 5 seam: it can call `joincode.New` and draw the QR code as inline SVG later), and the default repository `cravv/cravv-connect` in `install.sh` and the README is a placeholder until the real repository exists.

## Review Focus

These failure modes follow from the spec but no task's happy path exercises them. Each is pinned by the named test in its owning task.

1. **A join code for another relay pairs through the wrong relay, or burns a password attempt first.** `join` must refuse a join code whose relay differs from the daemon's with a clear message, before asking for the password and before `join.start`. Test: `TestJoinRefusesOtherRelay` (Task 3).
2. **A hostile join code points a new machine at a relay over public plain http.** The relay rule must apply before any question is asked or anything is written, while loopback, RFC 1918, ULA, Tailscale and `<name>.local` relays over http stay allowed. Test: `TestSetupJoinRelayRule` (Task 5).
3. **The LAN relay's admin token leaks to other local users.** The token setup generates must reach the relay only through its environment (never its arguments, which `ps` shows) and must be the one stored for the first registration. Test: `TestSetupStartsLANRelay` (Task 4).
4. **Setup silently moves or wipes a machine.** A re-run with another relay must refuse with the `--reset` advice, and `--reset` must ask first, change nothing when declined, and stop the daemon before re-initializing. Test: `TestSetupResetAsksFirst` (Task 4).
5. **A tampered or unlisted download gets installed.** A download that does not match `SHA256SUMS`, or has no entry there, must leave nothing installed. Test: `TestInstallChecksTheDownload` (Task 7).

## File Structure

Production files (tests live next to them as `*_test.go`; each task lists its test files).

| File | Change | Responsibility |
|---|---|---|
| `internal/relayaddr/relayaddr.go` | Create | `Check` and `PrivateHost`: the relay URL rule for addresses from other machines. |
| `internal/daemon/relayurl.go` | Modify | `validRelayURL` delegates to `relayaddr.Check`. |
| `internal/joincode/joincode.go` | Create | Join code format, forgiving parse, bind code for this machine's relay. |
| `internal/qrcode/qrcode.go` | Create | Terminal QR drawing over `rsc.io/qr`. |
| `internal/cli/joincode.go` | Create | `daemonRelay`, `printPairingCode`, `joinBindCode`. |
| `internal/cli/cmd_pair.go` | Modify | `pair --no-qr` shows join codes; `join` takes either form; `pairNow`, `joinNow`. |
| `internal/cli/cmd_setup.go` | Create | `setup` command, options, the host flow, daemon and relay waits, agents, pairing offer. |
| `internal/cli/setup_join.go` | Create | `setup --join`: join code checks and the joining flow. |
| `internal/cli/setup_lanrelay.go` | Create | LAN test relay: host name choice, start, health wait. |
| `internal/cli/setup_system.go` | Create | `SetupSystem` and the real network and process implementation. |
| `internal/cli/env.go` | Modify | `Env.Setup`. |
| `internal/cli/cmd_init.go` | Modify | The "Next:" hint prints from `init`'s `RunE` only. |
| `internal/cli/cmd_version.go` | Create | `cravv-connect version`. |
| `.github/workflows/release.yml` | Create | Tagged release: four native builds, archives, `SHA256SUMS`, GitHub release. |
| `.github/workflows/ci.yml` | Create | vet, race tests and no-cgo build on Linux and macOS; relay-cf typecheck and tests. |
| `Makefile` | Modify | `make build` stamps `VERSION`. |
| `cmd/cravv-connect/release_test.go` | Create | Pins the release workflow's shape and its version stamp. |
| `scripts/install.sh` | Create | The install one-liner. |
| `scripts/install_test.go`, `scripts/readme_test.go` | Create | Fake release server harness; README quick start check. |
| `README.md` | Modify | Install and quick start around `setup`; CLI reference rows. |
| `go.mod`, `go.sum` | Modify | `rsc.io/qr v0.2.0`. |

## Seams for later phases (do not implement here)

- **Web UI (Phase 5).** The Devices page can show `joincode.New(relay, bind)` and a QR code rendered as inline SVG from the same `rsc.io/qr` modules (no external requests; `img-src 'self'` is not needed for inline SVG).
- **Installers.** An installer that adds `Installed() bool` is skipped by setup once set up (`installChecker`), without touching setup.
- **Repository.** When the real repository exists, update `repo=` in `scripts/install.sh`, the one-liner at its top and in the README (`TestReadmeQuickStart` keeps them equal), and the module path if it changes.
- **Docs (Phase 6).** The security docs should state the LAN test relay's limits (plain http on the LAN, in memory, admin token in its environment).

---

### Task 1: relayaddr: the relay URL rule for peers and join codes, shared with the daemon

A join code carries a relay URL chosen by another machine, exactly like a pairing payload or `control.relay_moved`. The daemon already had the rule for those (https, or plain http only for this machine or a private network); this task moves it into its own package so `setup --join` applies the very same rule.

**Files:**
- Create: `internal/relayaddr/relayaddr.go`
- Modify: `internal/daemon/relayurl.go`
- Test: `internal/relayaddr/relayaddr_test.go` (new)

**Interfaces:**

Consumes: nothing new.

Produces:

```go
// internal/relayaddr/relayaddr.go
func Check(raw string) error        // https with a host, or http only for PrivateHost
func PrivateHost(host string) bool  // localhost, loopback, RFC 1918, ULA, 100.64.0.0/10, single-label .local
```

**Design notes:**
- `internal/daemon/relayurl.go` keeps `validRelayURL` (its callers and tests are unchanged) and delegates to `relayaddr.Check`.
- `.local` counts as private only for a single label (`mac.local`), never `a.b.local`, so a public DNS name ending in `.local` is not trusted.

- [ ] **Step 1: Write the failing tests**

Create `internal/relayaddr/relayaddr_test.go`:

```go
package relayaddr

import (
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	good := []string{"https://relay.example", "https://relay.example:8443/x", "http://localhost:8787",
		"http://127.0.0.1:8787", "http://[::1]:8787", "http://192.168.1.10:8787", "http://10.0.0.5",
		"http://172.16.4.2:8787", "http://[fd12::1]:8787", "http://100.101.102.103:8787", "http://mac.local:8787",
		"http://MAC.LOCAL:8787", "http://[::ffff:192.168.1.10]:8787"}
	bad := []string{"", "ftp://x", "javascript:alert(1)", "https://", "http://relay.example",
		"http://localhost.evil.example", "relay.example", "http://8.8.8.8", "http://172.32.0.1",
		"http://local.evil.example", "http://192.168.1.10.evil.example", "http://a.b.local:8787",
		"http://.local", "https://user@relay.example"}
	for _, u := range good {
		if err := Check(u); err != nil {
			t.Errorf("Check(%q) = %v", u, err)
		}
	}
	for _, u := range bad {
		if err := Check(u); err == nil {
			t.Errorf("Check(%q) accepted", u)
		}
	}
	if err := Check("http://relay.example"); err == nil || !strings.Contains(err.Error(), "plain http is only allowed") {
		t.Errorf("public http: %v", err)
	}
}

func TestPrivateHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "::1": true, "10.1.2.3": true, "192.168.0.1": true,
		"100.64.0.1": true, "fd00::1": true, "gpu-box.local": true,
		"example.com": false, "8.8.8.8": false, "100.128.0.1": false, "a.b.local": false, "local": false,
	} {
		if got := PrivateHost(host); got != want {
			t.Errorf("PrivateHost(%q) = %v, want %v", host, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/relayaddr -run '^(TestCheck|TestPrivateHost)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
FAIL	github.com/cookwithcravv/cravv-connect/internal/relayaddr [build failed]
FAIL
# github.com/cookwithcravv/cravv-connect/internal/relayaddr [github.com/cookwithcravv/cravv-connect/internal/relayaddr.test]
internal/relayaddr/relayaddr_test.go:18:13: undefined: Check
internal/relayaddr/relayaddr_test.go:23:13: undefined: Check
internal/relayaddr/relayaddr_test.go:27:12: undefined: Check
internal/relayaddr/relayaddr_test.go:38:13: undefined: PrivateHost
```

- [ ] **Step 3: Implement**

Create `internal/relayaddr/relayaddr.go`:

```go
// Package relayaddr is the rule for relay URLs that come from somewhere other
// than the person at this machine's keyboard: a peer's pairing payload,
// control.relay_moved, or a join code typed or scanned from another machine.
package relayaddr

import (
	"errors"
	"net/netip"
	"net/url"
	"strings"
)

// cgnat is the carrier-grade NAT range that Tailscale uses for node addresses.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Check accepts https with a host, or plain http only for relays on this
// machine or a private network (loopback, RFC 1918 and unique local
// addresses, Tailscale's 100.64.0.0/10, and single-label mDNS ".local"
// names). The relay only ever sees ciphertext, so http on a private network
// exposes metadata to that network at most.
func Check(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil {
		return errors.New("invalid relay URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if PrivateHost(u.Hostname()) {
			return nil
		}
		return errors.New("invalid relay URL: plain http is only allowed for localhost or a private network address")
	}
	return errors.New("invalid relay URL: must be https")
}

// PrivateHost reports whether host names this machine or a private network:
// localhost, a loopback, private or CGNAT address, or a single-label ".local"
// name.
func PrivateHost(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" {
		return true
	}
	if strings.HasSuffix(h, ".local") && len(h) > len(".local") && !strings.Contains(strings.TrimSuffix(h, ".local"), ".") {
		return true
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || cgnat.Contains(addr)
}
```

Modify `internal/daemon/relayurl.go` (apply this change):

```diff
diff --git a/internal/daemon/relayurl.go b/internal/daemon/relayurl.go
index c00999d..7a7dd9e 100644
--- a/internal/daemon/relayurl.go
+++ b/internal/daemon/relayurl.go
@@ -1,50 +1,9 @@
 package daemon
 
-import (
-	"errors"
-	"net/netip"
-	"net/url"
-	"strings"
-)
-
-// cgnat is the carrier-grade NAT range that Tailscale uses for node addresses.
-var cgnat = netip.MustParsePrefix("100.64.0.0/10")
+import "github.com/cookwithcravv/cravv-connect/internal/relayaddr"
 
 // validRelayURL accepts a relay URL learned from a peer (pairing payload or
-// control.relay_moved): https with a host, or plain http only for relays on
-// this machine or a private network (loopback, RFC 1918 and unique local
-// addresses, Tailscale's 100.64.0.0/10, and mDNS ".local" names). The relay
-// only ever sees ciphertext, so http on a private network exposes metadata to
-// that network at most.
-func validRelayURL(raw string) error {
-	u, err := url.Parse(raw)
-	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil {
-		return errors.New("invalid relay URL")
-	}
-	switch u.Scheme {
-	case "https":
-		return nil
-	case "http":
-		if privateHost(u.Hostname()) {
-			return nil
-		}
-		return errors.New("invalid relay URL: plain http is only allowed for localhost or a private network address")
-	}
-	return errors.New("invalid relay URL: must be https")
-}
-
-func privateHost(host string) bool {
-	h := strings.ToLower(host)
-	if h == "localhost" {
-		return true
-	}
-	if strings.HasSuffix(h, ".local") && len(h) > len(".local") && !strings.Contains(strings.TrimSuffix(h, ".local"), ".") {
-		return true
-	}
-	addr, err := netip.ParseAddr(h)
-	if err != nil {
-		return false
-	}
-	addr = addr.Unmap()
-	return addr.IsLoopback() || addr.IsPrivate() || cgnat.Contains(addr)
-}
+// control.relay_moved). The rule is relayaddr.Check, shared with
+// `cravv-connect setup --join`: https, or plain http only for this machine or
+// a private network.
+func validRelayURL(raw string) error { return relayaddr.Check(raw) }
```

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./internal/relayaddr -run '^(TestCheck|TestPrivateHost)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/relayurl.go internal/relayaddr/relayaddr.go internal/relayaddr/relayaddr_test.go
git commit -m "relayaddr: the relay URL rule for peers and join codes, shared with the daemon

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: joincode: cravv-join codes carry the relay origin with the bind code

A join code is `cravv-join:<base32(relay origin)>:<nameplate>-<secret>` (spec section 8): everything a new machine needs, in one string. It is parsed forgivingly because it is copied from terminals, chats and QR scanners.

**Files:**
- Create: `internal/joincode/joincode.go`
- Test: `internal/joincode/joincode_test.go` (new)

**Interfaces:**

Consumes: `bindcode.Code`, `bindcode.Parse`, `relayproto.NormalizeOrigin`.

Produces:

```go
// internal/joincode/joincode.go
const Prefix = "cravv-join"
const MaxLen = 512
var ErrInvalid, ErrOtherRelay error
type Code struct { Relay string; Bind bindcode.Code }
func New(relay string, bind bindcode.Code) (Code, error)
func (c Code) String() string
func Is(s string) bool
func Parse(s string) (Code, error)
func BindCode(s, relay string) (string, error) // plain bind code unchanged; join code only for relay
```

**Design notes:**
- The relay part is base32 (lower case, no padding) so the whole code upper-cases into the QR alphanumeric set.
- `Parse` accepts any case, white space anywhere, and the secret with or without hyphens; it refuses non-ASCII input and anything over `MaxLen`.
- The relay origin is not secret (the relay sees it on every request); the secret half of the bind code is, as before.

- [ ] **Step 1: Write the failing tests**

Create `internal/joincode/joincode_test.go`:

```go
package joincode

import (
	"errors"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
)

var bind = bindcode.Code{Nameplate: "7K3F", Secret: "9QXMTR2A"}

func TestStringFormat(t *testing.T) {
	c, err := New("HTTPS://Relay.Example.com:443/", bind)
	if err != nil {
		t.Fatal(err)
	}
	if c.Relay != "https://relay.example.com" {
		t.Fatalf("relay %q, want the normalized origin", c.Relay)
	}
	if got, want := c.String(), "cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	c2, _ := New("http://mac.local:8787", bind)
	if got, want := c2.String(), "cravv-join:nb2hi4b2f4xw2yldfzwg6y3bnq5dqnzyg4:7K3F-9QXMTR2A"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestNewRefusesBadInput(t *testing.T) {
	if _, err := New("https://relay.example.com/v1", bind); err == nil {
		t.Error("relay with a path accepted")
	}
	if _, err := New("https://relay.example.com", bindcode.Code{}); err == nil {
		t.Error("zero bind code accepted")
	}
}

// Join codes are copied from terminals, chats and QR scanners: any case,
// spaces or line breaks anywhere, and the QR code carries it upper-cased.
func TestParseIsForgiving(t *testing.T) {
	c, _ := New("https://relay.example.com", bind)
	s := c.String()
	for _, in := range []string{
		s,
		strings.ToUpper(s),
		"  " + s + "\n",
		s[:20] + "\n  " + s[20:40] + " " + s[40:],
		strings.Replace(s, "7K3F-9QXMTR2A", "7k3f-9qxm-tr2a", 1),
		strings.Replace(s, "7K3F-9QXMTR2A", "7K3F9QXMTR2A", 1),
		strings.Replace(s, "cravv-join", "Cravv-Join", 1),
	} {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got != c {
			t.Errorf("Parse(%q) = %+v, want %+v", in, got, c)
		}
		if !Is(in) {
			t.Errorf("Is(%q) = false", in)
		}
	}
}

func TestParseRefusesGarbage(t *testing.T) {
	c, _ := New("https://relay.example.com", bind)
	s := c.String()
	for _, in := range []string{
		"",
		"CRAVV-7K3F-9QXM-TR2A",
		"cravv-join:" + strings.SplitN(s, ":", 3)[1],
		"cravv-joins:" + strings.SplitN(s, ":", 2)[1],
		s + ":extra",
		strings.Replace(s, "nb2h", "nb2!", 1), // not base32
		"cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33nf53dc:7K3F-9QXMTR2A", // https://relay.example.com/v1
		"cravv-join:mz2haorpf54a:7K3F-9QXMTR2A",                                  // ftp://x
		strings.Replace(s, "7K3F-9QXMTR2A", "7K3F-9QXM", 1),                      // secret too short
		strings.Replace(s, "7K3F-9QXMTR2A", "7K3F-9QXMTR2U", 1),                  // U is not Crockford
		strings.Replace(s, "cravv", "cravvı", 1),                                 // non-ASCII
		s + strings.Repeat(" ", MaxLen),
	} {
		if _, err := Parse(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", in, err)
		}
	}
}

func TestBindCode(t *testing.T) {
	c, _ := New("https://relay.example.com", bind)
	got, err := BindCode("cravv-7k3f-9qxm-tr2a", "https://relay.example.com")
	if err != nil || got != "cravv-7k3f-9qxm-tr2a" {
		t.Fatalf("plain bind code: %q %v, want it unchanged", got, err)
	}
	got, err = BindCode(c.String(), "HTTPS://relay.example.com:443")
	if err != nil || got != "CRAVV-7K3F-9QXM-TR2A" {
		t.Fatalf("join code: %q %v", got, err)
	}
	_, err = BindCode(c.String(), "https://other.example.com")
	if !errors.Is(err, ErrOtherRelay) || !strings.Contains(err.Error(), "https://relay.example.com") ||
		!strings.Contains(err.Error(), "https://other.example.com") {
		t.Fatalf("other relay: %v", err)
	}
	if _, err := BindCode(c.String(), ""); !errors.Is(err, ErrOtherRelay) {
		t.Fatalf("no relay configured: %v", err)
	}
	if _, err := BindCode("cravv-join:zz:7K3F-9QXMTR2A", "https://relay.example.com"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad join code: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/joincode -run '^(TestStringFormat|TestNewRefusesBadInput|TestParseIsForgiving|TestParseRefusesGarbage|TestBindCode)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
FAIL	github.com/cookwithcravv/cravv-connect/internal/joincode [build failed]
FAIL
# github.com/cookwithcravv/cravv-connect/internal/joincode [github.com/cookwithcravv/cravv-connect/internal/joincode.test]
internal/joincode/joincode_test.go:14:12: undefined: New
internal/joincode/joincode_test.go:24:11: undefined: New
internal/joincode/joincode_test.go:31:15: undefined: New
internal/joincode/joincode_test.go:34:15: undefined: New
internal/joincode/joincode_test.go:42:10: undefined: New
internal/joincode/joincode_test.go:53:15: undefined: Parse
internal/joincode/joincode_test.go:61:7: undefined: Is
internal/joincode/joincode_test.go:68:10: undefined: New
internal/joincode/joincode_test.go:82:27: undefined: MaxLen
internal/joincode/joincode_test.go:84:16: undefined: Parse
internal/joincode/joincode_test.go:84:16: too many errors
```

- [ ] **Step 3: Implement**

Create `internal/joincode/joincode.go`:

```go
// Package joincode builds and parses join codes: a bind code together with
// the relay it was made on, so a new machine needs nothing else to join.
//
//	cravv-join:<relay origin in base32, lower case, no padding>:<nameplate>-<secret>
//
// The relay origin is not secret (the relay sees it on every request); the
// secret half of the bind code is, exactly as in a plain bind code.
package joincode

import (
	"encoding/base32"
	"errors"
	"fmt"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// Prefix starts every join code.
const Prefix = "cravv-join"

// MaxLen bounds the input Parse looks at.
const MaxLen = 512

// ErrInvalid is returned for any string that is not a join code.
var ErrInvalid = errors.New("invalid join code (expected cravv-join:<relay>:<code>)")

// ErrOtherRelay is returned by BindCode for a join code made on a relay other
// than this machine's.
var ErrOtherRelay = errors.New("the join code is for another relay")

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Code is a parsed join code.
type Code struct {
	Relay string // normalized relay origin (relayproto.NormalizeOrigin)
	Bind  bindcode.Code
}

// New returns the join code for a bind code made on relay.
func New(relay string, bind bindcode.Code) (Code, error) {
	origin, err := relayproto.NormalizeOrigin(relay)
	if err != nil {
		return Code{}, fmt.Errorf("joincode: %w", err)
	}
	if len(bind.Nameplate) != bindcode.NameplateLen || len(bind.Secret) != bindcode.SecretLen {
		return Code{}, errors.New("joincode: invalid bind code")
	}
	return Code{Relay: origin, Bind: bind}, nil
}

// String renders "cravv-join:<base32 relay>:<nameplate>-<secret>".
func (c Code) String() string {
	return Prefix + ":" + strings.ToLower(b32.EncodeToString([]byte(c.Relay))) + ":" + c.Bind.Nameplate + "-" + c.Bind.Secret
}

// compact drops all white space, so codes broken across lines still parse.
func compact(s string) string { return strings.Join(strings.Fields(s), "") }

// Is reports whether s is meant as a join code rather than a plain bind code
// (it starts with "cravv-join:" in any case, white space ignored).
func Is(s string) bool {
	return strings.HasPrefix(strings.ToLower(compact(s)), Prefix+":")
}

// Parse accepts a join code in any case, with white space anywhere, and the
// bind code part with or without hyphens (bindcode.Parse rules).
func Parse(s string) (Code, error) {
	if len(s) > MaxLen {
		return Code{}, ErrInvalid
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return Code{}, ErrInvalid
		}
	}
	parts := strings.Split(compact(s), ":")
	if len(parts) != 3 || !strings.EqualFold(parts[0], Prefix) {
		return Code{}, ErrInvalid
	}
	raw, err := b32.DecodeString(strings.ToUpper(parts[1]))
	if err != nil {
		return Code{}, ErrInvalid
	}
	origin, err := relayproto.NormalizeOrigin(string(raw))
	if err != nil {
		return Code{}, fmt.Errorf("%w: the relay in it is not an http(s) origin", ErrInvalid)
	}
	bind, err := bindcode.Parse("CRAVV" + parts[2])
	if err != nil {
		return Code{}, ErrInvalid
	}
	return Code{Relay: origin, Bind: bind}, nil
}

// BindCode returns what to join with for s: a plain bind code unchanged (the
// daemon parses it), or the bind code inside a join code made on relay, the
// relay this machine uses. A join code for any other relay is refused with
// ErrOtherRelay.
func BindCode(s, relay string) (string, error) {
	if !Is(s) {
		return s, nil
	}
	c, err := Parse(s)
	if err != nil {
		return "", err
	}
	mine, err := relayproto.NormalizeOrigin(relay)
	if err != nil {
		mine = "no relay"
	}
	if mine != c.Relay {
		return "", fmt.Errorf("%w: it is for %s, but this machine uses %s", ErrOtherRelay, c.Relay, mine)
	}
	return c.Bind.String(), nil
}
```

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./internal/joincode -run '^(TestStringFormat|TestNewRefusesBadInput|TestParseIsForgiving|TestParseRefusesGarbage|TestBindCode)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/joincode/joincode.go internal/joincode/joincode_test.go
git commit -m "joincode: cravv-join codes carry the relay origin with the bind code

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: pair and join: join codes with a terminal QR code; join refuses another relay's code

`pair` now shows the join code (with a QR code drawn in the terminal) for a new machine and the plain bind code for a machine already set up. `join` takes either form and refuses a join code made on a relay other than the daemon's, before any password is asked.

**Files:**
- Create: `internal/cli/joincode.go`, `internal/qrcode/qrcode.go`
- Modify: `go.mod`, `go.sum`, `internal/cli/cmd_pair.go`
- Test: `internal/cli/joincode_test.go` (new), `internal/qrcode/qrcode_test.go` (new)

**Interfaces:**

Consumes: `joincode`, `bindcode.Parse`, `ipc.MethodStatus` (`StatusResult.RelayURL`), `withUnlock`, `finalizePeer`.

Produces:

```go
// internal/qrcode/qrcode.go
const Quiet = 2
func Terminal(w io.Writer, text string) error // two module rows per line, black on white

// internal/cli/joincode.go
func daemonRelay(ctx context.Context, c Caller) string
func printPairingCode(w io.Writer, relay, bind string, qr bool) error
func joinBindCode(ctx context.Context, c Caller, code string) (string, error)

// internal/cli/cmd_pair.go
func pairNow(ctx context.Context, env *Env, c Caller, qr bool) error // used by `setup` too
```

**Design notes:**
- QR encoding uses `rsc.io/qr` v0.2.0: pure Go (no cgo), BSD-3-Clause (checked in the module cache `LICENSE`), error correction level M. `internal/qrcode` only draws the modules.
- The QR text is the join code upper-cased (alphanumeric mode, smaller code). Every line is wrapped in `ESC[30;47m` ... `ESC[0m` so the code scans the same in dark and light terminals. It was checked by decoding the drawing with OpenCV (`cv2.QRCodeDetector`), also with a dark border around it.
- Without a usable relay (an older daemon, status failing), `pair` prints exactly the old bind code text, so `TestPairFlow` is unchanged.
- `pair --no-qr` leaves the QR code out.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/joincode_test.go`:

```go
package cli

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/qrcode"
)

const testJoinCode = "cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A" // https://relay.example.com

func pairDaemon(t *testing.T, relay string) *fakeDaemon {
	fd := pairDaemonUnstarted(t, relay)
	fd.start()
	return fd
}

// pairDaemonUnstarted answers status (with relay), pairing and joining.
func pairDaemonUnstarted(t *testing.T, relay string) *fakeDaemon {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{RelayURL: relay, RelayConnected: true})
	fd.reply(ipc.MethodPairStart, ipc.GateUnlock, ipc.PairStartResult{PendingID: "P1", Code: "CRAVV-7K3F-9QXM-TR2A"})
	fd.reply(ipc.MethodPairAwait, ipc.GateUnlock, ipc.PendingPeerResult{PendingID: "P1", SuggestedName: "gpu-box", MachineID: "m1"})
	fd.reply(ipc.MethodJoinStart, ipc.GateUnlock, ipc.PendingPeerResult{PendingID: "P2", SuggestedName: "mac", MachineID: "m2"})
	fd.reply(ipc.MethodPairFinalize, ipc.GateUnlock, ipc.PairFinalizeResult{Alias: "gpu-box"})
	return fd
}

// pair shows the join code with its QR code (upper case, for the compact
// alphanumeric mode) and the plain bind code for machines already set up.
func TestPairShowsJoinCodeAndQR(t *testing.T) {
	fd := pairDaemon(t, "https://relay.example.com")
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{""}}, "pair")
	if r.code != 0 {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	var qr bytes.Buffer
	if err := qrcode.Terminal(&qr, strings.ToUpper(testJoinCode)); err != nil {
		t.Fatal(err)
	}
	want := "" +
		"Join code: " + testJoinCode + "\n\n" +
		qr.String() + "\n" +
		"On a new machine run:\n" +
		"  cravv-connect setup --join " + testJoinCode + "\n" +
		"On a machine already set up for this relay run:\n" +
		"  cravv-connect join CRAVV-7K3F-9QXM-TR2A\n" +
		"The code works once and expires in 10 minutes.\n" +
		"Waiting for the other machine...\n"
	if !strings.HasPrefix(r.stdout, want) {
		t.Fatalf("stdout\n%s\nwant prefix\n%s", r.stdout, want)
	}
}

func TestPairNoQR(t *testing.T) {
	fd := pairDaemon(t, "https://relay.example.com")
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{""}}, "pair", "--no-qr")
	if r.code != 0 || strings.Contains(r.stdout, "\x1b") || !strings.Contains(r.stdout, "Join code: "+testJoinCode+"\n\nOn a new machine") {
		t.Fatalf("code %d stdout %q", r.code, r.stdout)
	}
}

// join takes a join code made on this machine's relay and joins with the
// bind code inside it.
func TestJoinAcceptsJoinCode(t *testing.T) {
	fd := pairDaemon(t, "https://relay.example.com")
	r := fd.run(&fakePrompter{passwords: []string{"pw"}, lines: []string{""}}, "join", strings.ToUpper(testJoinCode))
	if r.code != 0 {
		t.Fatalf("code %d stderr %s", r.code, r.stderr)
	}
	if got := fd.params(ipc.MethodJoinStart); got != `{"code":"CRAVV-7K3F-9QXM-TR2A"}` {
		t.Fatalf("join params %s", got)
	}
}

// A join code made on another relay is refused before any password is asked
// and before the daemon is asked to join.
func TestJoinRefusesOtherRelay(t *testing.T) {
	fd := pairDaemon(t, "https://other.example.com")
	p := &fakePrompter{}
	r := fd.run(p, "join", testJoinCode)
	want := "error: the join code is for another relay: it is for https://relay.example.com, but this machine uses https://other.example.com. " +
		"To move this machine to that relay, run `cravv-connect setup --reset --join <code>` (you pair again with every peer).\n"
	if r.code != 1 || r.stderr != want {
		t.Fatalf("code %d stderr %q", r.code, r.stderr)
	}
	if slices.Contains(fd.methods(), ipc.MethodJoinStart) || len(p.asked) != 0 {
		t.Fatalf("joined anyway: %v, asked %v", fd.methods(), p.asked)
	}
}
```

Create `internal/qrcode/qrcode_test.go`:

```go
package qrcode

import (
	"bytes"
	"strings"
	"testing"

	"rsc.io/qr"
)

const sample = "CRAVV-JOIN:NB2HI4DTHIXS64TFNRQXSLTFPBQW24DMMUXGG33N:7K3F-9QXMTR2A"

// The terminal drawing must be exactly the encoder's modules: two module rows
// per line, dark modules drawn in the foreground (black) on a light (white)
// background, with a light quiet zone all round.
func TestTerminalDrawsTheModules(t *testing.T) {
	var buf bytes.Buffer
	if err := Terminal(&buf, sample); err != nil {
		t.Fatal(err)
	}
	code, err := qr.Encode(sample, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	n := code.Size + 2*Quiet
	dark := func(x, y int) bool {
		x, y = x-Quiet, y-Quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != (n+1)/2 {
		t.Fatalf("%d lines, want %d", len(lines), (n+1)/2)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, colors) || !strings.HasSuffix(line, reset) {
			t.Fatalf("line %d is not wrapped in the color codes: %q", i, line)
		}
		cells := []rune(strings.TrimSuffix(strings.TrimPrefix(line, colors), reset))
		if len(cells) != n {
			t.Fatalf("line %d has %d cells, want %d", i, len(cells), n)
		}
		for x, r := range cells {
			top, bottom := dark(x, 2*i), dark(x, 2*i+1)
			want := map[[2]bool]rune{{false, false}: ' ', {true, false}: '▀', {false, true}: '▄', {true, true}: '█'}[[2]bool{top, bottom}]
			if r != want {
				t.Fatalf("line %d cell %d = %q, want %q", i, x, r, want)
			}
		}
	}
}

// A join code upper-cased fits the QR alphanumeric mode, which keeps the
// drawing small enough for an ordinary terminal.
func TestJoinCodeFitsATerminal(t *testing.T) {
	long := "CRAVV-JOIN:" + strings.Repeat("A", 104) + ":7K3F-9QXMTR2A" // a 64-character relay origin
	var buf bytes.Buffer
	if err := Terminal(&buf, long); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if w := len([]rune(strings.TrimSuffix(strings.TrimPrefix(lines[0], colors), reset))); w > 60 || len(lines) > 30 {
		t.Fatalf("drawing is %d wide and %d high", w, len(lines))
	}
}

func TestTerminalRefusesEmptyText(t *testing.T) {
	if err := Terminal(&bytes.Buffer{}, ""); err == nil {
		t.Fatal("empty text accepted")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/cli ./internal/qrcode -run '^(TestPairShowsJoinCodeAndQR|TestPairNoQR|TestJoinAcceptsJoinCode|TestJoinRefusesOtherRelay|TestTerminalDrawsTheModules|TestJoinCodeFitsATerminal|TestTerminalRefusesEmptyText)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
FAIL	github.com/cookwithcravv/cravv-connect/internal/qrcode [setup failed]
FAIL	github.com/cookwithcravv/cravv-connect/internal/cli [build failed]
FAIL
# github.com/cookwithcravv/cravv-connect/internal/qrcode
internal/qrcode/qrcode_test.go:8:2: no required module provides package rsc.io/qr; to add it:
	go get rsc.io/qr
github.com/cookwithcravv/cravv-connect/internal/qrcode: no non-test Go files in internal/qrcode
```

- [ ] **Step 3: Implement**

Create `internal/cli/joincode.go`:

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/joincode"
	"github.com/cookwithcravv/cravv-connect/internal/qrcode"
)

// daemonRelay returns the relay URL the daemon uses, or "" when it cannot
// tell (an older daemon, or no answer).
func daemonRelay(ctx context.Context, c Caller) string {
	var st ipc.StatusResult
	if err := c.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
		return ""
	}
	return st.RelayURL
}

// printPairingCode shows how another machine pairs with bind, a bind code
// made on relay: the join code (with its QR code when qr is set) for a new
// machine, and the plain bind code for one already set up for this relay.
// Without a usable relay it shows the bind code alone.
func printPairingCode(w io.Writer, relay, bind string, qr bool) error {
	b, err := bindcode.Parse(bind)
	var jc joincode.Code
	if err == nil {
		jc, err = joincode.New(relay, b)
	}
	if err != nil {
		fmt.Fprintf(w, "Bind code: %s\n\n", bind)
		fmt.Fprintln(w, "On the other machine run:")
		fmt.Fprintf(w, "  cravv-connect join %s\n", bind)
	} else {
		fmt.Fprintf(w, "Join code: %s\n\n", jc)
		if qr {
			// Upper case fits the QR alphanumeric mode; join codes parse in any case.
			if err := qrcode.Terminal(w, strings.ToUpper(jc.String())); err != nil {
				return err
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "On a new machine run:")
		fmt.Fprintf(w, "  cravv-connect setup --join %s\n", jc)
		fmt.Fprintln(w, "On a machine already set up for this relay run:")
		fmt.Fprintf(w, "  cravv-connect join %s\n", bind)
	}
	fmt.Fprintln(w, "The code works once and expires in 10 minutes.")
	return nil
}

// joinBindCode returns the bind code to join with for code: a plain bind code
// unchanged, or the one inside a join code made on the daemon's relay. A join
// code for another relay is refused with advice.
func joinBindCode(ctx context.Context, c Caller, code string) (string, error) {
	if !joincode.Is(code) {
		return code, nil
	}
	bind, err := joincode.BindCode(code, daemonRelay(ctx, c))
	if errors.Is(err, joincode.ErrOtherRelay) {
		return "", fmt.Errorf("%w. To move this machine to that relay, run `cravv-connect setup --reset --join <code>` (you pair again with every peer).", err)
	}
	return bind, err
}
```

Create `internal/qrcode/qrcode.go`:

```go
// Package qrcode draws QR codes for join codes. It uses rsc.io/qr (BSD-3-Clause,
// pure Go) for the encoding and draws the modules itself.
package qrcode

import (
	"bufio"
	"errors"
	"io"

	"rsc.io/qr"
)

// Quiet is the light border around the code, in modules.
const Quiet = 2

// ANSI black on white for every line, so the code scans the same in dark and
// light terminals (scanners want dark modules on a light background).
const (
	colors = "\x1b[30;47m"
	reset  = "\x1b[0m"
)

// grid returns the modules of text's QR code (error correction level M),
// true for dark, with the quiet zone included.
func grid(text string) ([][]bool, error) {
	if text == "" {
		return nil, errors.New("qrcode: empty text")
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	n := code.Size + 2*Quiet
	g := make([][]bool, n)
	for y := range g {
		g[y] = make([]bool, n)
		for x := range g[y] {
			cx, cy := x-Quiet, y-Quiet
			g[y][x] = cx >= 0 && cy >= 0 && cx < code.Size && cy < code.Size && code.Black(cx, cy)
		}
	}
	return g, nil
}

// Terminal writes text as a QR code for a terminal: each line holds two rows
// of modules drawn with Unicode half blocks. Upper-case text (digits, A to Z
// and " $%*+-./:") uses the compact alphanumeric mode.
func Terminal(w io.Writer, text string) error {
	g, err := grid(text)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	for y := 0; y < len(g); y += 2 {
		bw.WriteString(colors)
		for x := range g[y] {
			top, bottom := g[y][x], y+1 < len(g) && g[y+1][x]
			switch {
			case top && bottom:
				bw.WriteString("█")
			case top:
				bw.WriteString("▀")
			case bottom:
				bw.WriteString("▄")
			default:
				bw.WriteByte(' ')
			}
		}
		bw.WriteString(reset + "\n")
	}
	return bw.Flush()
}
```

Modify `internal/cli/cmd_pair.go` (apply this change):

```diff
diff --git a/internal/cli/cmd_pair.go b/internal/cli/cmd_pair.go
index eedbeb1..cbb9526 100644
--- a/internal/cli/cmd_pair.go
+++ b/internal/cli/cmd_pair.go
@@ -14,9 +14,10 @@ func init() {
 }
 
 func newPairCmd(env *Env) *cobra.Command {
-	return &cobra.Command{
+	var noQR bool
+	cmd := &cobra.Command{
 		Use:   "pair",
-		Short: "Create a one-time bind code for another machine (asks for your password)",
+		Short: "Create a one-time join code for another machine (asks for your password)",
 		Args:  cobra.NoArgs,
 		RunE: func(cmd *cobra.Command, _ []string) error {
 			ctx := cmd.Context()
@@ -25,33 +26,17 @@ func newPairCmd(env *Env) *cobra.Command {
 				return err
 			}
 			defer c.Close()
-			var start ipc.PairStartResult
-			if err := withUnlock(ctx, env, c, func() error {
-				return c.Call(ctx, ipc.MethodPairStart, nil, &start)
-			}); err != nil {
-				return err
-			}
-			w := env.Stdout
-			fmt.Fprintf(w, "Bind code: %s\n\n", start.Code)
-			fmt.Fprintln(w, "On the other machine run:")
-			fmt.Fprintf(w, "  cravv-connect join %s\n", start.Code)
-			fmt.Fprintln(w, "The code works once and expires in 10 minutes.")
-			fmt.Fprintln(w, "Waiting for the other machine...")
-			var pending ipc.PendingPeerResult
-			if err := withUnlock(ctx, env, c, func() error {
-				return c.Call(ctx, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: start.PendingID}, &pending)
-			}); err != nil {
-				return err
-			}
-			return finalizePeer(ctx, env, c, pending)
+			return pairNow(ctx, env, c, !noQR)
 		},
 	}
+	cmd.Flags().BoolVar(&noQR, "no-qr", false, "do not draw the QR code")
+	return cmd
 }
 
 func newJoinCmd(env *Env) *cobra.Command {
 	return &cobra.Command{
 		Use:   "join <code>",
-		Short: "Join another machine using its bind code (asks for your password)",
+		Short: "Join another machine using its join code or bind code (asks for your password)",
 		Args:  cobra.ExactArgs(1),
 		RunE: func(cmd *cobra.Command, args []string) error {
 			ctx := cmd.Context()
@@ -60,9 +45,13 @@ func newJoinCmd(env *Env) *cobra.Command {
 				return err
 			}
 			defer c.Close()
+			code, err := joinBindCode(ctx, c, args[0])
+			if err != nil {
+				return err
+			}
 			var pending ipc.PendingPeerResult
 			if err := withUnlock(ctx, env, c, func() error {
-				return c.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: args[0]}, &pending)
+				return c.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: code}, &pending)
 			}); err != nil {
 				return err
 			}
@@ -71,6 +60,29 @@ func newJoinCmd(env *Env) *cobra.Command {
 	}
 }
 
+// pairNow creates a bind code, shows it as a join code (and QR code when qr
+// is set), waits for the other machine and asks for its local name.
+func pairNow(ctx context.Context, env *Env, c Caller, qr bool) error {
+	var start ipc.PairStartResult
+	if err := withUnlock(ctx, env, c, func() error {
+		return c.Call(ctx, ipc.MethodPairStart, nil, &start)
+	}); err != nil {
+		return err
+	}
+	w := env.Stdout
+	if err := printPairingCode(w, daemonRelay(ctx, c), start.Code, qr); err != nil {
+		return err
+	}
+	fmt.Fprintln(w, "Waiting for the other machine...")
+	var pending ipc.PendingPeerResult
+	if err := withUnlock(ctx, env, c, func() error {
+		return c.Call(ctx, ipc.MethodPairAwait, ipc.PairAwaitParams{PendingID: start.PendingID}, &pending)
+	}); err != nil {
+		return err
+	}
+	return finalizePeer(ctx, env, c, pending)
+}
+
 // finalizePeer asks the human for a local alias. Pairing lets the two
 // machines discover shared sessions and ask for links; each link is decided
 // on its own.
```

Add the QR encoder to the module:

```bash
go get rsc.io/qr@v0.2.0
go mod tidy
```

Expected `go.mod` and `go.sum` change:

```diff
diff --git a/go.mod b/go.mod
index 0deb49f..402543e 100644
--- a/go.mod
+++ b/go.mod
@@ -11,6 +11,7 @@ require (
 	golang.org/x/sys v0.48.0
 	golang.org/x/term v0.46.0
 	modernc.org/sqlite v1.59.0
+	rsc.io/qr v0.2.0
 	salsa.debian.org/vasudev/gospake2 v0.0.0-20210510093858-d91629950ad1
 )
 
diff --git a/go.sum b/go.sum
index a79c6d4..5ef335f 100644
--- a/go.sum
+++ b/go.sum
@@ -84,5 +84,7 @@ modernc.org/strutil v1.2.1 h1:UneZBkQA+DX2Rp35KcM69cSsNES9ly8mQWD71HKlOA0=
 modernc.org/strutil v1.2.1/go.mod h1:EHkiggD70koQxjVdSBM3JKM7k6L0FbGE5eymy9i3B9A=
 modernc.org/token v1.1.0 h1:Xl7Ap9dKaEs5kLoOQeQmPWevfnk/DM5qcLcYlA8ys6Y=
 modernc.org/token v1.1.0/go.mod h1:UGzOrNV1mAFSEB63lOFHIpNRUVMvYTc6yu1SMY/XTDM=
+rsc.io/qr v0.2.0 h1:6vBLea5/NRMVTz8V66gipeLycZMl/+UlFmk8DvqQ6WY=
+rsc.io/qr v0.2.0/go.mod h1:IF+uZjkb9fqyeF/4tlBoynqmQxUoPfWEKh921coOuXs=
 salsa.debian.org/vasudev/gospake2 v0.0.0-20210510093858-d91629950ad1 h1:m65DhEZR/5zbgOGW4sQGDZmIwro+xBGIBQGWm43SlxM=
 salsa.debian.org/vasudev/gospake2 v0.0.0-20210510093858-d91629950ad1/go.mod h1:soKzqXBAtqHTODjyA0VzH2iERtpzN1w65eZUfetn2cQ=
```

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./internal/cli ./internal/qrcode -run '^(TestPairShowsJoinCodeAndQR|TestPairNoQR|TestJoinAcceptsJoinCode|TestJoinRefusesOtherRelay|TestTerminalDrawsTheModules|TestJoinCodeFitsATerminal|TestTerminalRefusesEmptyText)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/cli/cmd_pair.go internal/cli/joincode.go internal/cli/joincode_test.go internal/qrcode/qrcode.go internal/qrcode/qrcode_test.go
git commit -m "pair and join: join codes with a terminal QR code; join refuses another relay's code

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: setup: the host wizard (relay or LAN test relay, init, daemon, agents, pairing), safe to run again

`cravv-connect setup` walks a first machine from nothing to paired: relay URL or a LAN test relay, admin token, `init`, the daemon as a login service and a wait until it is connected to the relay, the agent integrations, and "pair a device now". Every step checks what is already done, so a re-run shows the state and offers only the missing steps. `setup --reset` starts over after confirmation.

**Files:**
- Create: `internal/cli/cmd_setup.go`, `internal/cli/setup_lanrelay.go`, `internal/cli/setup_system.go`
- Modify: `internal/cli/cmd_init.go`, `internal/cli/env.go`
- Test: `internal/cli/setup_test.go` (new)

**Interfaces:**

Consumes: `Prompter` (`Line`, `Password`), `runInit`, `relayOrigin`, `relayaddr.Check`, `daemonUp`, `newDaemonInstallCmd`, `newDaemonStopCmd`, `daemonLogHint`, `install.Registry` and `install.Installer` (interface only), `pairNow`, `withConn`, `ipc.StatusResult`.

Produces:

```go
// internal/cli/setup_system.go
type SetupSystem interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	PrivateAddrs() ([]netip.Addr, error)
	RelayHealthy(ctx context.Context, origin string) error
	StartRelay(bin string, args, extraEnv []string, logPath string) error
}
// internal/cli/env.go
type Env struct { ...; Setup SetupSystem } // nil uses realSystem

// internal/cli/cmd_setup.go
type setupOptions struct { relay, token, name string; yes, noAgents bool; pair, reset bool }
var setupOnlineWait = 30 * time.Second
func runSetup(ctx context.Context, env *Env, o setupOptions) error
func confirm(p Prompter, question string, def bool) (bool, error)
type installChecker interface{ Installed() bool } // optional, for installers that can tell

// internal/cli/setup_lanrelay.go
const lanRelayPort = 8787
var relayStartWait = 5 * time.Second
func (s *setup) lanHost() string
func (s *setup) startLANRelay() (origin, token string, err error)
```

**Design notes:**
- Flags: `--relay`, `--relay-token` (`-` reads stdin, as in `init`), `--name`, `--yes`/`-y`, `--no-agents`, `--pair`, `--reset`.
- `--yes` answers yes to every question but never starts a LAN relay and never pairs (pairing needs a person on the other machine); on a new machine it needs `--relay`.
- Relay URLs typed into setup must pass `relayaddr.Check` (other machines would refuse join codes for a public plain-http relay; `init` still accepts one) and answer `GET /v1/health`.
- LAN test relay: `cravv-relay` next to the (symlink-resolved) `cravv-connect`, `-addr 0.0.0.0:8787 -origin http://<host>:8787`, started in its own session with its output in `~/.cravv-connect/relay.log`. The admin token is 32 random bytes (base64url) passed only in `CRAVV_RELAY_ADMIN_TOKEN`, never on the command line (`ps`). `<host>.local` is used when the host name is a DNS label and resolves; otherwise the first private IPv4 address; otherwise 127.0.0.1. A relay already answering there is refused.
- The daemon step reuses `daemon install` and `daemon stop` by running their `RunE`, so `cmd_install.go` and `cmd_daemon.go` are untouched. `init` moves its "Next:" line from `runInit` to its `RunE`, so setup does not print it.
- A daemon that does not reach the relay within `setupOnlineWait` fails setup with the daemon's errors, the advice to use `setup --reset --join <code>` if another machine is already on the relay, and where the logs are.
- Agents: every detected installer is offered (default yes). The installers have no "already installed" check today and `internal/install/claude.go` and `codex.go` must not change in this phase, so setup relies on `Install` being idempotent and skips an installer only if it implements the optional `installChecker`.
- `--reset` asks "Set this machine up again? ... (y/N)", stops a running daemon, and runs `init --force`, which clears the old relay's registration when the relay changes.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/setup_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/install"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// serve starts the fake daemon and returns a function that stops it; the
// daemon can be served again afterwards (a restart).
func (fd *fakeDaemon) serve() (stop func()) {
	ln, err := ipc.Listen(fd.sock)
	if err != nil {
		fd.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { fd.srv.Serve(ctx, ln); close(done) }()
	var once sync.Once
	stop = func() { once.Do(func() { cancel(); <-done }) }
	fd.t.Cleanup(stop)
	return stop
}

// daemonProcess is the login service around a fake daemon: Install and
// Start serve it, Stop stops it. Every call is logged in events.
type daemonProcess struct {
	fd     *fakeDaemon
	mu     sync.Mutex
	stop   func()
	events []string
}

func (d *daemonProcess) log(e string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, e)
}

func (d *daemonProcess) up() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stop == nil {
		d.stop = d.fd.serve()
	}
}

func (d *daemonProcess) Installed() bool { return true }
func (d *daemonProcess) Install(_ context.Context, bin string) error {
	d.log("install " + bin)
	d.up()
	return nil
}
func (d *daemonProcess) Uninstall(context.Context) error { d.log("uninstall"); return nil }
func (d *daemonProcess) Start(context.Context) error     { d.log("start"); d.up(); return nil }
func (d *daemonProcess) Stop(context.Context) error {
	d.log("stop")
	d.mu.Lock()
	stop := d.stop
	d.stop = nil
	d.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

type startedRelay struct {
	bin       string
	args, env []string
	logPath   string
}

// fakeSystem is SetupSystem without a network: names in resolves resolve,
// origins in healthy answer, and a started relay answers at its -origin.
type fakeSystem struct {
	mu       sync.Mutex
	resolves map[string][]string
	private  []netip.Addr
	healthy  map[string]bool
	startErr error
	started  []startedRelay
	lookups  []string
}

func (f *fakeSystem) LookupHost(_ context.Context, host string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups = append(f.lookups, host)
	if a, ok := f.resolves[host]; ok {
		return a, nil
	}
	return nil, errors.New("no such host")
}

func (f *fakeSystem) PrivateAddrs() ([]netip.Addr, error) { return f.private, nil }

func (f *fakeSystem) RelayHealthy(_ context.Context, origin string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.healthy[origin] {
		return nil
	}
	return errors.New("connection refused")
}

func (f *fakeSystem) StartRelay(bin string, args, env []string, logPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, startedRelay{bin, args, env, logPath})
	if i := slices.Index(args, "-origin"); i >= 0 {
		f.healthy[args[i+1]] = true
	}
	return nil
}

type setupRig struct {
	fd       *fakeDaemon
	env      *Env
	out      *bytes.Buffer
	errb     *bytes.Buffer
	prompt   *fakePrompter
	sys      *fakeSystem
	settings mapSettings
	daemon   *daemonProcess
	claude   *fakeInstaller
	codex    *fakeInstaller
}

// newSetupRig builds a machine for setup: a fake daemon that answers once
// its login service is installed or started, claude detected and codex not,
// and https://relay.example.com answering.
func newSetupRig(t *testing.T, st ipc.StatusResult) *setupRig {
	fd := newFakeDaemon(t)
	fd.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, st)
	return newSetupRigOn(t, fd)
}

func newSetupRigOn(t *testing.T, fd *fakeDaemon) *setupRig {
	r := &setupRig{
		fd: fd, prompt: &fakePrompter{}, settings: mapSettings{},
		sys:    &fakeSystem{healthy: map[string]bool{"https://relay.example.com": true}},
		daemon: &daemonProcess{fd: fd},
		claude: &fakeInstaller{name: "claude", detected: true},
		codex:  &fakeInstaller{name: "codex"},
	}
	r.env, r.out, r.errb = fd.env(r.prompt, "")
	r.env.OpenSettings = func(string) (store.SettingsStore, func() error, error) {
		return r.settings, func() error { return nil }, nil
	}
	r.env.Executable = func() (string, error) { return "/usr/local/bin/cravv-connect", nil }
	r.env.Service, r.env.ServiceSetup = r.daemon, r.daemon
	r.env.Agents = install.NewRegistry(r.claude, r.codex)
	r.env.Setup = r.sys
	return r
}

func (r *setupRig) run(args ...string) int {
	r.out.Reset()
	r.errb.Reset()
	return Main(append([]string{"setup"}, args...), r.env)
}

func (r *setupRig) config(t *testing.T) config.Config {
	t.Helper()
	paths, _ := r.env.Paths()
	cfg, err := config.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func (r *setupRig) configure(t *testing.T, relay string) {
	t.Helper()
	paths, _ := r.env.Paths()
	cfg := config.Defaults()
	cfg.RelayURL, cfg.DeviceName = relay, "mac"
	if err := config.Save(paths, cfg); err != nil {
		t.Fatal(err)
	}
}

func (r *setupRig) notConfigured(t *testing.T) {
	t.Helper()
	paths, _ := r.env.Paths()
	if _, configured, _ := (&setup{paths: paths}).current(); configured {
		t.Fatal("setup wrote a configuration")
	}
}

var connected = ipc.StatusResult{RelayURL: "https://relay.example.com", RelayConnected: true}

// The first machine on a relay: a typed relay URL and admin token, init,
// the login service, the detected agent, and no pairing when declined.
func TestSetupFirstMachine(t *testing.T) {
	r := newSetupRig(t, connected)
	r.prompt.lines = []string{"https://relay.example.com", "", "n"}
	r.prompt.passwords = []string{"admin-tok"}
	if code := r.run(); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if cfg := r.config(t); cfg.RelayURL != "https://relay.example.com" || cfg.DeviceName != "prith-s-macbook" {
		t.Fatalf("config %+v", cfg)
	}
	if r.settings[SettingRelayAdminToken] != "admin-tok" {
		t.Fatalf("admin token %q", r.settings[SettingRelayAdminToken])
	}
	if !slices.Equal(r.daemon.events, []string{"install /usr/local/bin/cravv-connect"}) {
		t.Fatalf("service %v", r.daemon.events)
	}
	if r.claude.installed != "/usr/local/bin/cravv-connect" || r.codex.installed != "" {
		t.Fatalf("claude %q codex %q", r.claude.installed, r.codex.installed)
	}
	wantAsked := []string{
		"line: Relay URL (press Enter to start a LAN test relay here)",
		"password: Relay admin token (only for the relay's first machine; press Enter if another machine is already on it): ",
		"line: Add cravv-connect to claude? (Y/n)",
		"line: Pair a device now? (Y/n)",
	}
	if !slices.Equal(r.prompt.asked, wantAsked) {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	for _, want := range []string{
		"\n== Relay ==\n", "\n== This machine ==\nInitialized cravv-connect in ",
		"\n== Daemon ==\nDaemon installed as a login service and started.\n",
		"Connected to the relay https://relay.example.com.\n",
		"\n== Agents ==\n", "Installed cravv-connect for claude. Restart it so it loads the MCP server.\n",
		"codex: not found on this machine, skipped.\n",
		"\n== Pair a device ==\nLater: `cravv-connect pair` shows a join code for the other machine.\n",
		"\nSetup is complete. Restart your agents, then type /cravv in Claude Code to share a chat.\n",
	} {
		if !strings.Contains(r.out.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, r.out.String())
		}
	}
	if strings.Contains(r.out.String(), "Next: run `cravv-connect daemon install`") {
		t.Error("setup printed init's next step")
	}
}

// --yes asks nothing: the relay comes from --relay, every detected agent is
// set up, and nobody is paired.
func TestSetupYesAsksNothing(t *testing.T) {
	r := newSetupRig(t, connected)
	if code := r.run("--yes", "--relay", "https://relay.example.com", "--relay-token", "tok", "--name", "Build Box"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if len(r.prompt.asked) != 0 {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	if cfg := r.config(t); cfg.DeviceName != "build-box" || r.settings[SettingRelayAdminToken] != "tok" || r.claude.installed == "" {
		t.Fatalf("config %+v token %q claude %q", cfg, r.settings[SettingRelayAdminToken], r.claude.installed)
	}
}

// A relay setup cannot use is refused before anything is written.
func TestSetupRelayChecks(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--yes"}, "error: setup --yes needs --relay <url> on a machine that is not set up yet\n"},
		{[]string{"--relay", "http://relay.example.com"}, "error: invalid relay URL: plain http is only allowed for localhost or a private network address: " +
			"other machines refuse join codes for it. Use https, or set it anyway with `cravv-connect init`\n"},
		{[]string{"--relay", "https://down.example.com"}, "error: the relay https://down.example.com does not answer: connection refused\n"},
		{[]string{"--relay", "https://relay.example.com/v1"}, "error: relay must be scheme://host[:port] with no path, query or user: "},
	} {
		r := newSetupRig(t, connected)
		if code := r.run(tc.args...); code != 1 || !strings.HasPrefix(r.errb.String(), tc.want) {
			t.Errorf("%v: code %d stderr %q", tc.args, code, r.errb.String())
		}
		r.notConfigured(t)
		if len(r.daemon.events) != 0 {
			t.Errorf("%v: service %v", tc.args, r.daemon.events)
		}
	}
}

// An empty relay answer starts cravv-relay from next to this binary on
// <host>.local, with the admin token only in its environment.
func TestSetupStartsLANRelay(t *testing.T) {
	r := newSetupRig(t, ipc.StatusResult{RelayURL: "http://prithvis-mac.local:8787", RelayConnected: true})
	r.env.Hostname = func() (string, error) { return "Prithvis-Mac.lan", nil }
	r.sys.resolves = map[string][]string{"prithvis-mac.local": {"192.168.1.10"}}
	r.prompt.lines = []string{"", "n"}
	if code := r.run("--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if len(r.sys.started) != 1 {
		t.Fatalf("started %v", r.sys.started)
	}
	s := r.sys.started[0]
	if s.bin != "/usr/local/bin/cravv-relay" || !slices.Equal(s.args, []string{"-addr", "0.0.0.0:8787", "-origin", "http://prithvis-mac.local:8787"}) {
		t.Fatalf("started %s %q", s.bin, s.args)
	}
	token, ok := strings.CutPrefix(strings.Join(s.env, ""), "CRAVV_RELAY_ADMIN_TOKEN=")
	if !ok || len(s.env) != 1 || len(token) != 43 {
		t.Fatalf("relay env %q", s.env)
	}
	if r.settings[SettingRelayAdminToken] != token {
		t.Fatalf("stored admin token %q, relay has %q", r.settings[SettingRelayAdminToken], token)
	}
	if cfg := r.config(t); cfg.RelayURL != "http://prithvis-mac.local:8787" {
		t.Fatalf("relay %q", cfg.RelayURL)
	}
	if !strings.HasSuffix(s.logPath, "/relay.log") || !strings.Contains(r.out.String(), "Started a LAN test relay at http://prithvis-mac.local:8787 (log: ") {
		t.Fatalf("log %s\n%s", s.logPath, r.out.String())
	}
	if slices.Contains(r.prompt.asked, "password: Relay admin token (only for the relay's first machine; press Enter if another machine is already on it): ") {
		t.Fatal("asked for the admin token of the relay it started")
	}
}

// <host>.local when it resolves, else the first private IPv4 address, else
// loopback; a host name that is not a DNS label is never looked up.
func TestLANRelayHost(t *testing.T) {
	v4, v6 := netip.MustParseAddr("192.168.1.23"), netip.MustParseAddr("fd00::1")
	for _, tc := range []struct {
		host     string
		resolves map[string][]string
		private  []netip.Addr
		want     string
	}{
		{"Mac.lan", map[string][]string{"mac.local": {"192.168.1.23"}}, []netip.Addr{v4}, "mac.local"},
		{"gpu-box", nil, []netip.Addr{v6, v4}, "192.168.1.23"},
		{"Prith's MacBook", map[string][]string{"prith's macbook.local": {"x"}}, []netip.Addr{v6}, "fd00::1"},
		{"gpu-box", nil, nil, "127.0.0.1"},
	} {
		sys := &fakeSystem{resolves: tc.resolves, private: tc.private}
		s := &setup{ctx: context.Background(), sys: sys, env: &Env{Hostname: func() (string, error) { return tc.host, nil }}}
		if got := s.lanHost(); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.host, got, tc.want)
		}
		for _, l := range sys.lookups {
			if strings.ContainsAny(l, "' ") {
				t.Errorf("%s: looked up %q", tc.host, l)
			}
		}
	}
}

func TestSetupLANRelayProblems(t *testing.T) {
	r := newSetupRig(t, connected)
	r.env.Hostname = func() (string, error) { return "mac", nil }
	r.sys.resolves = map[string][]string{"mac.local": {"192.168.1.10"}}
	r.sys.startErr = &fs.PathError{Op: "fork/exec", Path: "/usr/local/bin/cravv-relay", Err: fs.ErrNotExist}
	r.prompt.lines = []string{""}
	if code := r.run(); code != 1 || r.errb.String() != "error: cravv-relay was not found next to cravv-connect (/usr/local/bin/cravv-relay); install it from the release archive, or enter a relay URL\n" {
		t.Fatalf("missing binary: %d %q", code, r.errb.String())
	}
	r.notConfigured(t)

	r2 := newSetupRig(t, connected)
	r2.env.Hostname = func() (string, error) { return "mac", nil }
	r2.sys.resolves = map[string][]string{"mac.local": {"192.168.1.10"}}
	r2.sys.healthy["http://mac.local:8787"] = true
	r2.prompt.lines = []string{""}
	if code := r2.run(); code != 1 || !strings.Contains(r2.errb.String(), "a relay already answers at http://mac.local:8787. If another machine is on it, set this one up with `cravv-connect setup --join <code>`") || len(r2.sys.started) != 0 {
		t.Fatalf("already running: %d %q", code, r2.errb.String())
	}
}

// Running setup again shows what is set up and offers only what is missing:
// no init, the daemon when it is not running, the agents, and pairing
// (default no once there are peers).
func TestSetupRerunOffersMissingSteps(t *testing.T) {
	st := connected
	st.Peers = []ipc.PeerView{{Alias: "gpu-box"}}
	r := newSetupRig(t, st)
	r.configure(t, "https://relay.example.com")
	r.prompt.lines = []string{"", "n", ""}
	if code := r.run(); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	out := r.out.String()
	if !strings.HasPrefix(out, "This machine is set up for relay https://relay.example.com as mac.\n") ||
		!strings.Contains(out, "Paired with gpu-box.\n") || !strings.Contains(out, "claude: skipped. Later: `cravv-connect install claude`.\n") {
		t.Fatalf("stdout\n%s", out)
	}
	wantAsked := []string{
		"line: The daemon is not running. Install it as a login service and start it? (Y/n)",
		"line: Add cravv-connect to claude? (Y/n)",
		"line: Pair a device now? (y/N)",
	}
	if !slices.Equal(r.prompt.asked, wantAsked) || len(r.daemon.events) != 1 || r.claude.installed != "" {
		t.Fatalf("asked %q service %v", r.prompt.asked, r.daemon.events)
	}
	if r.config(t).DeviceName != "mac" {
		t.Fatal("setup ran init again")
	}

	// Once the daemon runs, it is only reported.
	r.prompt.lines, r.prompt.asked = []string{"n"}, nil
	if code := r.run("--no-agents"); code != 0 || !strings.Contains(r.out.String(), "== Daemon ==\nDaemon is running.\nConnected to the relay") || len(r.daemon.events) != 1 {
		t.Fatalf("second run: %d %s %v", code, r.out.String(), r.daemon.events)
	}
}

// A machine set up for one relay is never moved to another without --reset.
func TestSetupRefusesAnotherRelayWithoutReset(t *testing.T) {
	r := newSetupRig(t, connected)
	r.configure(t, "https://relay.example.com")
	r.sys.healthy["https://other.example.com"] = true
	if code := r.run("--relay", "https://other.example.com"); code != 1 || r.errb.String() != "error: this machine is set up for relay https://relay.example.com; "+
		"to move it to https://other.example.com, run `cravv-connect setup --reset --relay https://other.example.com` (you pair again with every peer)\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if r.config(t).RelayURL != "https://relay.example.com" || len(r.daemon.events) != 0 {
		t.Fatal("setup changed the machine")
	}
}

// --reset asks first; yes stops the daemon, runs init again (clearing the
// old relay's registration) and installs the daemon again.
func TestSetupResetAsksFirst(t *testing.T) {
	r := newSetupRig(t, ipc.StatusResult{RelayURL: "https://other.example.com", RelayConnected: true})
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	r.sys.healthy["https://other.example.com"] = true
	r.prompt.lines = []string{"n"}
	if code := r.run("--reset", "--relay", "https://other.example.com", "--no-agents"); code != 0 || r.out.String() != "Nothing changed.\n" {
		t.Fatalf("declined: %d %q", code, r.out.String())
	}
	if r.config(t).RelayURL != "https://relay.example.com" || len(r.daemon.events) != 0 {
		t.Fatal("a declined reset changed the machine")
	}
	if r.prompt.asked[0] != "line: Set this machine up again? It is set up for relay https://relay.example.com; on a new relay you pair again with every peer. (y/N)" {
		t.Fatalf("asked %q", r.prompt.asked)
	}

	r.prompt.lines, r.prompt.passwords = []string{"y", "n"}, []string{""}
	r.settings[settingRelayRegistered] = "1"
	if code := r.run("--reset", "--relay", "https://other.example.com", "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if r.config(t).RelayURL != "https://other.example.com" || r.settings[settingRelayRegistered] != "" {
		t.Fatalf("config %+v settings %v", r.config(t), r.settings)
	}
	if !slices.Equal(r.daemon.events, []string{"stop", "install /usr/local/bin/cravv-connect"}) {
		t.Fatalf("service %v", r.daemon.events)
	}
}

// Setup waits for the relay connection, and when it does not come says why
// and what to do instead.
func TestSetupWaitsForTheRelay(t *testing.T) {
	defer func(d time.Duration) { setupOnlineWait = d }(setupOnlineWait)
	setupOnlineWait = 10 * time.Second
	fd := newFakeDaemon(t)
	var mu sync.Mutex
	calls := 0
	fd.handle(ipc.MethodStatus, ipc.GateAllowWhenKilled, func(*ipc.ConnState, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return ipc.StatusResult{RelayURL: "https://relay.example.com", RelayConnected: calls > 4}, nil
	})
	r := newSetupRigOn(t, fd)
	if code := r.run("--yes", "--relay", "https://relay.example.com", "--relay-token", "tok"); code != 0 ||
		!strings.Contains(r.out.String(), "Waiting for the relay connection...\nConnected to the relay https://relay.example.com.\n") {
		t.Fatalf("code %d stdout %s stderr %s", code, r.out.String(), r.errb.String())
	}

	setupOnlineWait = 300 * time.Millisecond
	fd2 := newFakeDaemon(t)
	fd2.reply(ipc.MethodStatus, ipc.GateAllowWhenKilled, ipc.StatusResult{RelayURL: "https://relay.example.com", Errors: []string{"relay: registration refused"}})
	r2 := newSetupRigOn(t, fd2)
	if code := r2.run("--yes", "--relay", "https://relay.example.com"); code != 1 ||
		!strings.Contains(r2.errb.String(), "error: the daemon did not connect to the relay https://relay.example.com within 300ms; relay: registration refused. "+
			"If another machine is already on this relay, set this one up with `cravv-connect setup --reset --join <code>` instead") {
		t.Fatalf("code %d stderr %q", code, r2.errb.String())
	}
	if r2.claude.installed != "" {
		t.Fatal("went on to the agents without a relay connection")
	}
}

// --pair pairs without asking and shows the join code.
func TestSetupPairFlag(t *testing.T) {
	fd := pairDaemonUnstarted(t, "https://relay.example.com")
	r := newSetupRigOn(t, fd)
	r.configure(t, "https://relay.example.com")
	r.daemon.up()
	r.prompt.passwords, r.prompt.lines = []string{"pw"}, []string{""}
	if code := r.run("--pair", "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if !strings.Contains(r.out.String(), "== Pair a device ==\nJoin code: "+testJoinCode+"\n") || !strings.Contains(r.out.String(), "Paired with gpu-box.") {
		t.Fatalf("stdout\n%s", r.out.String())
	}
	if slices.Contains(r.prompt.asked, "line: Pair a device now? (Y/n)") {
		t.Fatal("asked although --pair was given")
	}
}

// The real system: a missing relay binary is fs.ErrNotExist (setup turns it
// into advice), and only a cravv relay's health answer counts as healthy.
func TestRealSetupSystem(t *testing.T) {
	dir := t.TempDir()
	err := realSystem{}.StartRelay(filepath.Join(dir, "cravv-relay"), nil, nil, filepath.Join(dir, "relay.log"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing binary: %v", err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != relayproto.PathHealth {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"ok":true,"version":1}`))
	}))
	defer relay.Close()
	if err := (realSystem{}).RelayHealthy(context.Background(), relay.URL); err != nil {
		t.Fatalf("relay: %v", err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) }))
	defer other.Close()
	if err := (realSystem{}).RelayHealthy(context.Background(), other.URL); err == nil {
		t.Fatal("a web server passed for a relay")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/cli -run '^(TestSetupFirstMachine|TestSetupYesAsksNothing|TestSetupRelayChecks|TestSetupStartsLANRelay|TestLANRelayHost|TestSetupLANRelayProblems|TestSetupRerunOffersMissingSteps|TestSetupRefusesAnotherRelayWithoutReset|TestSetupResetAsksFirst|TestSetupWaitsForTheRelay|TestSetupPairFlag|TestRealSetupSystem)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
FAIL	github.com/cookwithcravv/cravv-connect/internal/cli [build failed]
FAIL
# github.com/cookwithcravv/cravv-connect/internal/cli [github.com/cookwithcravv/cravv-connect/internal/cli.test]
internal/cli/setup_test.go:174:8: r.env.Setup undefined (type *Env has no field or method Setup)
internal/cli/setup_test.go:207:27: undefined: setup
internal/cli/setup_test.go:351:9: undefined: setup
internal/cli/setup_test.go:468:54: undefined: setupOnlineWait
internal/cli/setup_test.go:469:2: undefined: setupOnlineWait
internal/cli/setup_test.go:485:2: undefined: setupOnlineWait
internal/cli/setup_test.go:521:9: undefined: realSystem
internal/cli/setup_test.go:533:13: undefined: realSystem
internal/cli/setup_test.go:538:13: undefined: realSystem
```

- [ ] **Step 3: Implement**

Create `internal/cli/cmd_setup.go`:

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/relayaddr"
	"github.com/spf13/cobra"
)

func init() { Register(newSetupCmd) }

// setupOptions are the flags of `cravv-connect setup`.
type setupOptions struct {
	relay, token, name string
	yes, noAgents      bool
	pair, reset        bool
}

func newSetupCmd(env *Env) *cobra.Command {
	var o setupOptions
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Set this machine up step by step: relay, daemon, agents, pairing (safe to run again)",
		Long: "Set this machine up step by step: the relay (or a LAN test relay), init, the daemon as a login service,\n" +
			"the agent integrations, and pairing a device. Run it again at any time: it shows what is set up and\n" +
			"offers the missing steps. On another machine, use `cravv-connect setup --join <code>` with the join\n" +
			"code this machine shows.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd.Context(), env, o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.relay, "relay", "", "relay URL, for example https://relay.example.com")
	f.StringVar(&o.token, "relay-token", "", "relay admin token, only for the relay's first machine; use - to read it from stdin")
	f.StringVar(&o.name, "name", "", "device name suggested to peers (default: host name)")
	f.BoolVarP(&o.yes, "yes", "y", false, "answer yes to every question (needs --relay on a new machine)")
	f.BoolVar(&o.noAgents, "no-agents", false, "do not set up agent integrations")
	f.BoolVar(&o.pair, "pair", false, "pair a device at the end without asking")
	f.BoolVar(&o.reset, "reset", false, "set this machine up again from the start (asks first)")
	return cmd
}

// setupOnlineWait is how long setup waits for the daemon to reach the relay.
var setupOnlineWait = 30 * time.Second

// setup runs the wizard. Each step checks what is already done, so running
// it again only offers what is missing.
type setup struct {
	ctx   context.Context
	env   *Env
	sys   SetupSystem
	o     setupOptions
	w     io.Writer
	paths config.Paths
}

func runSetup(ctx context.Context, env *Env, o setupOptions) error {
	paths, err := env.Paths()
	if err != nil {
		return err
	}
	s := &setup{ctx: ctx, env: env, sys: env.setupSystem(), o: o, w: env.Stdout, paths: paths}
	cfg, configured, err := s.current()
	if err != nil {
		return err
	}
	if o.reset && configured {
		ok, err := s.agree(fmt.Sprintf("Set this machine up again? It is set up for relay %s; on a new relay you pair again with every peer.", cfg.RelayURL), false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(s.w, "Nothing changed.")
			return nil
		}
		if err := s.stopDaemon(); err != nil {
			return err
		}
		configured = false
	}
	return s.hostFlow(cfg, configured)
}

// current loads this machine's configuration; configured is false when no
// config.toml exists yet.
func (s *setup) current() (config.Config, bool, error) {
	if _, err := os.Stat(s.paths.Config); errors.Is(err, os.ErrNotExist) {
		return config.Config{}, false, nil
	}
	cfg, err := config.Load(s.paths)
	return cfg, err == nil, err
}

func (s *setup) hostFlow(cfg config.Config, configured bool) error {
	if configured {
		if s.o.relay != "" {
			want, err := relayOrigin(s.o.relay)
			if err != nil {
				return err
			}
			if have, _ := relayOrigin(cfg.RelayURL); have != want {
				return fmt.Errorf("this machine is set up for relay %s; to move it to %s, run `cravv-connect setup --reset --relay %s` (you pair again with every peer)", cfg.RelayURL, want, want)
			}
		}
		fmt.Fprintf(s.w, "This machine is set up for relay %s as %s.\n", cfg.RelayURL, cfg.DeviceName)
	} else {
		s.section("Relay")
		relay, token, err := s.chooseRelay()
		if err != nil {
			return err
		}
		s.section("This machine")
		if err := runInit(s.ctx, s.env, relay, token, s.o.name, s.o.reset); err != nil {
			return err
		}
	}
	st, err := s.daemonOnline(!configured, true)
	if err != nil {
		return err
	}
	if err := s.agents(); err != nil {
		return err
	}
	if err := s.offerPairing(st); err != nil {
		return err
	}
	s.done()
	return nil
}

// section starts a step of the wizard.
func (s *setup) section(title string) { fmt.Fprintf(s.w, "\n== %s ==\n", title) }

// agree asks a yes or no question; --yes answers yes.
func (s *setup) agree(question string, def bool) (bool, error) {
	if s.o.yes {
		return true, nil
	}
	return confirm(s.env.Prompt, question, def)
}

// confirm asks a yes or no question until it gets an answer; an empty
// answer is def.
func confirm(p Prompter, question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		a, err := p.Line(question+" ("+hint+")", "")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(a)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// chooseRelay returns the relay origin and the admin token to init with:
// the --relay URL, one typed at the prompt, or a LAN test relay started on
// this machine.
func (s *setup) chooseRelay() (string, string, error) {
	relay := s.o.relay
	if relay == "" {
		if s.o.yes {
			return "", "", errors.New("setup --yes needs --relay <url> on a machine that is not set up yet")
		}
		fmt.Fprintln(s.w, "cravv-connect talks through a relay. For real use, deploy the Cloudflare relay (relay-cf/README.md);")
		fmt.Fprintln(s.w, "to try it on this network, this machine can run a test relay.")
		a, err := s.env.Prompt.Line("Relay URL (press Enter to start a LAN test relay here)", "")
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(a) == "" {
			return s.startLANRelay()
		}
		relay = strings.TrimSpace(a)
	}
	origin, err := relayOrigin(relay)
	if err != nil {
		return "", "", err
	}
	if err := relayaddr.Check(origin); err != nil {
		return "", "", fmt.Errorf("%w: other machines refuse join codes for it. Use https, or set it anyway with `cravv-connect init`", err)
	}
	if err := s.sys.RelayHealthy(s.ctx, origin); err != nil {
		return "", "", fmt.Errorf("the relay %s does not answer: %w", origin, err)
	}
	token := s.o.token
	if token == "" && !s.o.yes {
		token, err = s.env.Prompt.Password("Relay admin token (only for the relay's first machine; press Enter if another machine is already on it): ")
		if err != nil {
			return "", "", err
		}
	}
	return origin, strings.TrimSpace(token), nil
}

// daemonOnline makes sure the daemon runs and is connected to the relay.
// install installs it as a login service without asking (a fresh setup);
// otherwise a daemon that is not running is offered. needRelay waits for the
// relay connection.
func (s *setup) daemonOnline(install, needRelay bool) (ipc.StatusResult, error) {
	s.section("Daemon")
	if !daemonUp(s.ctx, s.env) {
		if !install {
			ok, err := s.agree("The daemon is not running. Install it as a login service and start it?", true)
			if err != nil {
				return ipc.StatusResult{}, err
			}
			if !ok {
				return ipc.StatusResult{}, errors.New("setup needs the daemon: run `cravv-connect daemon install` or `cravv-connect daemon run`")
			}
		}
		if err := s.run(newDaemonInstallCmd); err != nil {
			return ipc.StatusResult{}, err
		}
	} else {
		fmt.Fprintln(s.w, "Daemon is running.")
	}
	st, err := s.status()
	if err != nil || !needRelay {
		return st, err
	}
	if !st.RelayConnected {
		fmt.Fprintln(s.w, "Waiting for the relay connection...")
	}
	deadline := time.Now().Add(setupOnlineWait)
	for !st.RelayConnected {
		if time.Now().After(deadline) {
			msg := fmt.Sprintf("the daemon did not connect to the relay %s within %s", st.RelayURL, setupOnlineWait)
			for _, e := range st.Errors {
				msg += "; " + terminalSafe(e)
			}
			return st, fmt.Errorf("%s. If another machine is already on this relay, set this one up with "+
				"`cravv-connect setup --reset --join <code>` instead (a join code from `cravv-connect pair` there). Logs: %s",
				msg, daemonLogHint(s.env))
		}
		select {
		case <-s.ctx.Done():
			return st, s.ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if st, err = s.status(); err != nil {
			return st, err
		}
	}
	fmt.Fprintf(s.w, "Connected to the relay %s.\n", terminalSafe(st.RelayURL))
	return st, nil
}

func (s *setup) status() (ipc.StatusResult, error) {
	var st ipc.StatusResult
	err := withConn(s.ctx, s.env, func(c Caller) error { return c.Call(s.ctx, ipc.MethodStatus, nil, &st) })
	return st, err
}

// run runs another command's RunE (daemon install, daemon stop) as a step.
func (s *setup) run(f Factory) error {
	cmd := f(s.env)
	cmd.SetContext(s.ctx)
	return cmd.RunE(cmd, nil)
}

// stopDaemon stops a running daemon so it restarts with the new setup.
func (s *setup) stopDaemon() error {
	if !daemonUp(s.ctx, s.env) {
		return nil
	}
	return s.run(newDaemonStopCmd)
}

// installChecker is implemented by installers that can tell whether their
// integration is already in place; setup then skips them.
type installChecker interface{ Installed() bool }

// agents offers the integration for every detected agent.
func (s *setup) agents() error {
	s.section("Agents")
	if s.o.noAgents {
		fmt.Fprintln(s.w, "Skipped (--no-agents). Later: `cravv-connect install <agent>`.")
		return nil
	}
	if s.env.Agents == nil {
		fmt.Fprintln(s.w, "Agent integrations are not available in this build.")
		return nil
	}
	bin, err := s.env.Executable()
	if err != nil {
		return err
	}
	for _, name := range s.env.Agents.Names() {
		i, _ := s.env.Agents.Get(name)
		if !i.Detect() {
			fmt.Fprintf(s.w, "%s: not found on this machine, skipped.\n", name)
			continue
		}
		if c, ok := i.(installChecker); ok && c.Installed() {
			fmt.Fprintf(s.w, "%s: already set up.\n", name)
			continue
		}
		ok, err := s.agree(fmt.Sprintf("Add cravv-connect to %s?", name), true)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(s.w, "%s: skipped. Later: `cravv-connect install %s`.\n", name, name)
			continue
		}
		if err := i.Install(s.ctx, bin); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(s.w, "Installed cravv-connect for %s. Restart it so it loads the MCP server.\n", name)
	}
	return nil
}

// offerPairing pairs a device when --pair is set, or when asked (the
// default is yes while there are no peers). --yes alone never pairs:
// pairing needs a person on the other machine.
func (s *setup) offerPairing(st ipc.StatusResult) error {
	s.section("Pair a device")
	if len(st.Peers) > 0 {
		names := make([]string, 0, len(st.Peers))
		for _, p := range st.Peers {
			names = append(names, terminalSafe(p.Alias))
		}
		fmt.Fprintf(s.w, "Paired with %s.\n", strings.Join(names, ", "))
	}
	pair := s.o.pair
	if !pair && !s.o.yes {
		var err error
		if pair, err = confirm(s.env.Prompt, "Pair a device now?", len(st.Peers) == 0); err != nil {
			return err
		}
	}
	if !pair {
		fmt.Fprintln(s.w, "Later: `cravv-connect pair` shows a join code for the other machine.")
		return nil
	}
	return withConn(s.ctx, s.env, func(c Caller) error { return pairNow(s.ctx, s.env, c, true) })
}

func (s *setup) done() {
	fmt.Fprintln(s.w, "\nSetup is complete. Restart your agents, then type /cravv in Claude Code to share a chat.")
}
```

Create `internal/cli/setup_lanrelay.go`:

```go
package cli

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// lanRelayPort is the port of the LAN test relay setup starts.
const lanRelayPort = 8787

// relayStartWait is how long setup waits for the LAN test relay to answer.
var relayStartWait = 5 * time.Second

// hostLabel is a host name label that can be looked up as <label>.local.
var hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// lanHost names this machine for other machines on the network: <host>.local
// when it resolves (it survives DHCP address changes), else the first private
// address, else 127.0.0.1 (this machine only).
func (s *setup) lanHost() string {
	if h, err := s.env.Hostname(); err == nil {
		label, _, _ := strings.Cut(h, ".")
		if hostLabel.MatchString(label) {
			name := strings.ToLower(label) + ".local"
			if addrs, err := s.sys.LookupHost(s.ctx, name); err == nil && len(addrs) > 0 {
				return name
			}
		}
	}
	if addrs, err := s.sys.PrivateAddrs(); err == nil {
		for _, a := range addrs {
			if a.Is4() {
				return a.String()
			}
		}
		if len(addrs) > 0 {
			return addrs[0].String()
		}
	}
	return "127.0.0.1"
}

// startLANRelay starts the Go reference relay (cravv-relay, next to this
// binary) on every interface, with a fresh admin token passed in its
// environment (never on its command line), and waits until it answers. It
// returns the relay origin and the admin token.
func (s *setup) startLANRelay() (string, string, error) {
	exe, err := s.env.Executable()
	if err != nil {
		return "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	bin := filepath.Join(filepath.Dir(exe), "cravv-relay")
	host := s.lanHost()
	origin := "http://" + net.JoinHostPort(host, strconv.Itoa(lanRelayPort))
	if s.sys.RelayHealthy(s.ctx, origin) == nil {
		return "", "", fmt.Errorf("a relay already answers at %s. If another machine is on it, set this one up with `cravv-connect setup --join <code>` "+
			"(a join code from `cravv-connect pair` there); if an earlier setup started it, stop it (pkill -x cravv-relay) and run setup again", origin)
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	if err := os.MkdirAll(s.paths.Home, 0o700); err != nil {
		return "", "", err
	}
	logPath := filepath.Join(s.paths.Home, "relay.log")
	args := []string{"-addr", net.JoinHostPort("0.0.0.0", strconv.Itoa(lanRelayPort)), "-origin", origin}
	if err := s.sys.StartRelay(bin, args, []string{"CRAVV_RELAY_ADMIN_TOKEN=" + token}, logPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("cravv-relay was not found next to cravv-connect (%s); install it from the release archive, or enter a relay URL", bin)
		}
		return "", "", err
	}
	deadline := time.Now().Add(relayStartWait)
	for {
		err := s.sys.RelayHealthy(s.ctx, origin)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return "", "", fmt.Errorf("the LAN test relay did not answer at %s within %s: %w (see %s)", origin, relayStartWait, err, logPath)
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(s.w, "Started a LAN test relay at %s (log: %s).\n", origin, logPath)
	fmt.Fprintln(s.w, "It keeps everything in memory and stops when this machine restarts; for real use, deploy the Cloudflare relay.")
	if host == "127.0.0.1" {
		fmt.Fprintln(s.w, "No network address was found, so only this machine can reach it.")
	}
	return origin, token, nil
}
```

Create `internal/cli/setup_system.go`:

```go
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// SetupSystem is what `cravv-connect setup` needs from the network and the
// process table beyond Env. Tests replace it; nil in Env means realSystem.
type SetupSystem interface {
	// LookupHost resolves a host name (used for <host>.local).
	LookupHost(ctx context.Context, host string) ([]string, error)
	// PrivateAddrs returns this machine's private network addresses
	// (RFC 1918 and unique local), in interface order.
	PrivateAddrs() ([]netip.Addr, error)
	// RelayHealthy checks that a cravv relay answers GET /v1/health at origin.
	RelayHealthy(ctx context.Context, origin string) error
	// StartRelay starts bin in the background, in its own session, with
	// extraEnv added to this process's environment and its output appended
	// to logPath. A missing bin is an error matching fs.ErrNotExist.
	StartRelay(bin string, args, extraEnv []string, logPath string) error
}

func (env *Env) setupSystem() SetupSystem {
	if env.Setup != nil {
		return env.Setup
	}
	return realSystem{}
}

// realSystem is SetupSystem on the real network and process table.
type realSystem struct{}

func (realSystem) LookupHost(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

func (realSystem) PrivateAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if ok && ip.Unmap().IsPrivate() {
			out = append(out, ip.Unmap())
		}
	}
	return out, nil
}

// healthTimeout bounds one relay health check.
const healthTimeout = 5 * time.Second

func (realSystem) RelayHealthy(ctx context.Context, origin string) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+relayproto.PathHealth, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var h relayproto.Health
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h) != nil || !h.OK {
		return errors.New("it does not answer like a cravv relay (GET /v1/health)")
	}
	return nil
}

func (realSystem) StartRelay(bin string, args, extraEnv []string, logPath string) error {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	return cmd.Process.Release()
}
```

Modify `internal/cli/cmd_init.go` (apply this change):

```diff
diff --git a/internal/cli/cmd_init.go b/internal/cli/cmd_init.go
index 64be77b..8998c18 100644
--- a/internal/cli/cmd_init.go
+++ b/internal/cli/cmd_init.go
@@ -37,7 +37,11 @@ func newInitCmd(env *Env) *cobra.Command {
 		Short: "Configure this machine (relay URL, device name, first-machine admin token)",
 		Args:  cobra.NoArgs,
 		RunE: func(cmd *cobra.Command, _ []string) error {
-			return runInit(cmd.Context(), env, relay, token, name, force)
+			if err := runInit(cmd.Context(), env, relay, token, name, force); err != nil {
+				return err
+			}
+			fmt.Fprintln(env.Stdout, "Next: run `cravv-connect daemon install` (starts at login) or `cravv-connect daemon run`.")
+			return nil
 		},
 	}
 	cmd.Flags().StringVar(&relay, "relay", "", "relay URL, for example https://relay.example.com (required)")
@@ -157,6 +161,5 @@ func runInit(ctx context.Context, env *Env, relay, token, name string, force boo
 			"(this version does not send control.relay_moved): re-pair with each of them on the new relay. "+
 			"Restart the daemon to use it.\n", oldRelay)
 	}
-	fmt.Fprintln(w, "Next: run `cravv-connect daemon install` (starts at login) or `cravv-connect daemon run`.")
 	return nil
 }
```

Modify `internal/cli/env.go` (apply this change):

```diff
diff --git a/internal/cli/env.go b/internal/cli/env.go
index 77de0d2..17d3634 100644
--- a/internal/cli/env.go
+++ b/internal/cli/env.go
@@ -49,6 +49,7 @@ type Env struct {
 	Service      ServiceManager
 	ServiceSetup ServiceInstaller  // installs the login service; nil when unsupported
 	Agents       *install.Registry // agent installers; nil disables `install`
+	Setup        SetupSystem       // network and processes for `setup`; nil uses the real ones
 }
 
 // ServiceInstaller installs and removes the login service.
```

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./internal/cli -run '^(TestSetupFirstMachine|TestSetupYesAsksNothing|TestSetupRelayChecks|TestSetupStartsLANRelay|TestLANRelayHost|TestSetupLANRelayProblems|TestSetupRerunOffersMissingSteps|TestSetupRefusesAnotherRelayWithoutReset|TestSetupResetAsksFirst|TestSetupWaitsForTheRelay|TestSetupPairFlag|TestRealSetupSystem)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/cmd_init.go internal/cli/cmd_setup.go internal/cli/env.go internal/cli/setup_lanrelay.go internal/cli/setup_system.go internal/cli/setup_test.go
git commit -m "setup: the host wizard (relay or LAN test relay, init, daemon, agents, pairing), safe to run again

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: setup --join: join the relay in a join code after confirming it, then pair and set up the agents

On every other machine, `setup --join <code>` does everything: it shows the relay in the code and asks "Join relay <origin>? (y/N)", refuses plain http unless the relay is on this machine or a private network (the `relayaddr` rule), refuses a machine already set up for another relay unless `--reset`, then runs init, the daemon, join (password), the alias prompt and the agent integrations.

**Files:**
- Create: `internal/cli/setup_join.go`
- Modify: `internal/cli/cmd_pair.go`, `internal/cli/cmd_setup.go`
- Test: `internal/cli/setup_join_test.go` (new)

**Interfaces:**

Consumes: `joincode.Is`, `joincode.Parse`, `relayaddr.Check`, the Task 4 `setup` steps (`daemonOnline`, `agents`, `done`), `finalizePeer`.

Produces:

```go
// internal/cli/setup_join.go
func setupJoinCode(s string) (joincode.Code, error)
func (s *setup) joinFlow(c joincode.Code, cfg config.Config, configured bool) error
// internal/cli/cmd_setup.go
func (s *setup) waitRelay(st ipc.StatusResult, advice string) (ipc.StatusResult, error)
// internal/cli/cmd_pair.go
func joinNow(ctx context.Context, env *Env, c Caller, code string) error
```

**Design notes:**
- The code is parsed and the relay rule applied before anything is asked (also before the `--reset` question).
- A joining machine has no mailbox until the invite arrives while joining, so the daemon step does not wait for the relay; setup waits for the relay connection after the join.
- On a machine already set up for the same relay, nothing is re-initialized and the relay question is not asked: it just joins.
- `--join` cannot be combined with `--relay`, `--relay-token` or `--pair` (cobra mutually exclusive flags).
- `waitRelay` is split out of `daemonOnline` so both flows share it with their own advice.

- [ ] **Step 1: Write the failing tests**

Create `internal/cli/setup_join_test.go`:

```go
package cli

import (
	"slices"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/joincode"
)

func joinRig(t *testing.T) *setupRig {
	return newSetupRigOn(t, pairDaemonUnstarted(t, "https://relay.example.com"))
}

func codeFor(t *testing.T, relay string) string {
	t.Helper()
	c, err := joincode.New(relay, bindcode.Code{Nameplate: "7K3F", Secret: "9QXMTR2A"})
	if err != nil {
		t.Fatal(err)
	}
	return c.String()
}

// A new machine: the relay is shown and confirmed, then init (no admin
// token), the login service, join with the password, the alias, and the
// agents, in that order.
func TestSetupJoinNewMachine(t *testing.T) {
	r := joinRig(t)
	r.prompt.lines = []string{"y", "", ""}
	r.prompt.passwords = []string{"pw"}
	if code := r.run("--join", testJoinCode); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	wantAsked := []string{
		"line: Join relay https://relay.example.com? (y/N)",
		"password: " + passwordPrompt,
		"line: Local name for this peer",
		"line: Add cravv-connect to claude? (Y/n)",
	}
	if !slices.Equal(r.prompt.asked, wantAsked) {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	if got := r.fd.params(ipc.MethodJoinStart); got != `{"code":"CRAVV-7K3F-9QXM-TR2A"}` {
		t.Fatalf("join params %s", got)
	}
	if cfg := r.config(t); cfg.RelayURL != "https://relay.example.com" {
		t.Fatalf("relay %q", cfg.RelayURL)
	}
	if _, ok := r.settings[SettingRelayAdminToken]; ok {
		t.Fatal("a joining machine stored an admin token")
	}
	if !slices.Equal(r.daemon.events, []string{"install /usr/local/bin/cravv-connect"}) || r.claude.installed == "" {
		t.Fatalf("service %v claude %q", r.daemon.events, r.claude.installed)
	}
	out := r.out.String()
	for _, want := range []string{"\n== Join ==\nConnected to machine m2.\nPaired with gpu-box.", "Connected to the relay https://relay.example.com.\n\n== Agents ==", "Setup is complete."} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

// Declining the relay changes nothing.
func TestSetupJoinDeclined(t *testing.T) {
	r := joinRig(t)
	r.prompt.lines = []string{""}
	if code := r.run("--join", testJoinCode); code != 0 || !strings.HasSuffix(r.out.String(), "Nothing changed.\n") {
		t.Fatalf("code %d stdout %q", code, r.out.String())
	}
	r.notConfigured(t)
	if len(r.daemon.events) != 0 || slices.Contains(r.fd.methods(), ipc.MethodJoinStart) {
		t.Fatal("joined anyway")
	}
}

// The relay in a join code comes from another machine: plain http is refused
// unless it is this machine or a private network, before anything is asked.
func TestSetupJoinRelayRule(t *testing.T) {
	for relay, ok := range map[string]bool{
		"http://relay.example.com":   false,
		"http://203.0.113.9:8787":    false,
		"http://192.168.1.10:8787":   true,
		"http://mac.local:8787":      true,
		"http://100.100.1.2:8787":    true,
		"https://relay.example.com":  true,
		"http://[fd00::1]:8787":      true,
		"http://relay.example.local": false,
	} {
		_, err := setupJoinCode(codeFor(t, relay))
		if (err == nil) != ok {
			t.Errorf("%s: %v", relay, err)
		}
	}
	r := joinRig(t)
	if code := r.run("--join", codeFor(t, "http://relay.example.com")); code != 1 ||
		r.errb.String() != "error: refusing the relay in this join code (http://relay.example.com): invalid relay URL: plain http is only allowed for localhost or a private network address\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if len(r.prompt.asked) != 0 {
		t.Fatalf("asked %q", r.prompt.asked)
	}
	r.notConfigured(t)
}

// A machine set up for another relay needs --reset; one set up for the same
// relay just joins (no init, no relay question).
func TestSetupJoinOnASetUpMachine(t *testing.T) {
	r := joinRig(t)
	r.configure(t, "https://other.example.com")
	if code := r.run("--join", testJoinCode); code != 1 || r.errb.String() != "error: this machine is set up for relay https://other.example.com, "+
		"but the join code is for https://relay.example.com; to move it, run `cravv-connect setup --reset --join <code>` (you pair again with every peer)\n" {
		t.Fatalf("code %d stderr %q", code, r.errb.String())
	}
	if len(r.prompt.asked) != 0 || r.config(t).RelayURL != "https://other.example.com" {
		t.Fatal("setup changed the machine")
	}

	r2 := joinRig(t)
	r2.configure(t, "https://relay.example.com")
	r2.daemon.up()
	r2.prompt.lines, r2.prompt.passwords = []string{"laptop"}, []string{"pw"}
	if code := r2.run("--join", testJoinCode, "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r2.errb.String())
	}
	if !slices.Equal(r2.prompt.asked, []string{"password: " + passwordPrompt, "line: Local name for this peer"}) || len(r2.daemon.events) != 0 {
		t.Fatalf("asked %q service %v", r2.prompt.asked, r2.daemon.events)
	}
	if !strings.Contains(r2.fd.params(ipc.MethodPairFinalize), `"alias":"laptop"`) || r2.config(t).DeviceName != "mac" {
		t.Fatalf("finalize %s", r2.fd.params(ipc.MethodPairFinalize))
	}
}

// --reset --join moves a machine to the join code's relay after both
// questions.
func TestSetupResetJoin(t *testing.T) {
	r := joinRig(t)
	r.configure(t, "https://other.example.com")
	r.daemon.up()
	r.prompt.lines, r.prompt.passwords = []string{"y", "y", ""}, []string{"pw"}
	if code := r.run("--reset", "--join", testJoinCode, "--no-agents"); code != 0 {
		t.Fatalf("code %d stderr %s", code, r.errb.String())
	}
	if r.config(t).RelayURL != "https://relay.example.com" || !slices.Equal(r.daemon.events, []string{"stop", "install /usr/local/bin/cravv-connect"}) {
		t.Fatalf("relay %q service %v", r.config(t).RelayURL, r.daemon.events)
	}
}

func TestSetupJoinInputs(t *testing.T) {
	r := joinRig(t)
	if code := r.run("--join", "CRAVV-7K3F-9QXM-TR2A"); code != 1 ||
		r.errb.String() != "error: setup --join needs a join code (cravv-join:...): run `cravv-connect pair` on the other machine to show one\n" {
		t.Fatalf("bind code: %d %q", code, r.errb.String())
	}
	if code := r.run("--join", "cravv-join:!!:7K3F-9QXMTR2A"); code != 1 || !strings.Contains(r.errb.String(), "invalid join code") {
		t.Fatalf("garbage: %d %q", code, r.errb.String())
	}
	if code := r.run("--join", testJoinCode, "--relay", "https://relay.example.com"); code != 1 || !strings.Contains(r.errb.String(), "[join relay] were all set") {
		t.Fatalf("with --relay: %d %q", code, r.errb.String())
	}
	r.notConfigured(t)
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./internal/cli -run '^(TestSetupJoinNewMachine|TestSetupJoinDeclined|TestSetupJoinRelayRule|TestSetupJoinOnASetUpMachine|TestSetupResetJoin|TestSetupJoinInputs)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
FAIL	github.com/cookwithcravv/cravv-connect/internal/cli [build failed]
FAIL
# github.com/cookwithcravv/cravv-connect/internal/cli [github.com/cookwithcravv/cravv-connect/internal/cli.test]
internal/cli/setup_join_test.go:91:13: undefined: setupJoinCode
```

- [ ] **Step 3: Implement**

Create `internal/cli/setup_join.go`:

```go
package cli

import (
	"errors"
	"fmt"

	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/joincode"
	"github.com/cookwithcravv/cravv-connect/internal/relayaddr"
)

// setupJoinCode parses the code given to `setup --join` and applies the relay
// rule: https, or plain http only for this machine or a private network.
func setupJoinCode(s string) (joincode.Code, error) {
	if !joincode.Is(s) {
		return joincode.Code{}, errors.New("setup --join needs a join code (cravv-join:...): run `cravv-connect pair` on the other machine to show one")
	}
	c, err := joincode.Parse(s)
	if err != nil {
		return joincode.Code{}, err
	}
	if err := relayaddr.Check(c.Relay); err != nil {
		return joincode.Code{}, fmt.Errorf("refusing the relay in this join code (%s): %w", c.Relay, err)
	}
	return c, nil
}

// joinFlow sets this machine up on the relay of a join code and pairs it with
// the machine that showed the code: init, the daemon, join (password), the
// alias, then the agent integrations. A machine set up for another relay is
// refused unless --reset.
func (s *setup) joinFlow(c joincode.Code, cfg config.Config, configured bool) error {
	if configured {
		have, err := relayOrigin(cfg.RelayURL)
		if err != nil {
			have = cfg.RelayURL
		}
		if have != c.Relay {
			return fmt.Errorf("this machine is set up for relay %s, but the join code is for %s; to move it, run `cravv-connect setup --reset --join <code>` (you pair again with every peer)", have, c.Relay)
		}
		fmt.Fprintf(s.w, "This machine is set up for relay %s as %s.\n", cfg.RelayURL, cfg.DeviceName)
	} else {
		s.section("Relay")
		ok, err := s.agree(fmt.Sprintf("Join relay %s?", c.Relay), false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(s.w, "Nothing changed.")
			return nil
		}
		if err := s.sys.RelayHealthy(s.ctx, c.Relay); err != nil {
			return fmt.Errorf("the relay %s does not answer: %w", c.Relay, err)
		}
		s.section("This machine")
		if err := runInit(s.ctx, s.env, c.Relay, "", s.o.name, s.o.reset); err != nil {
			return err
		}
	}
	// No mailbox yet: the invite arrives while joining.
	if _, err := s.daemonOnline(!configured, false); err != nil {
		return err
	}
	s.section("Join")
	if err := withConn(s.ctx, s.env, func(cl Caller) error { return joinNow(s.ctx, s.env, cl, c.Bind.String()) }); err != nil {
		return err
	}
	st, err := s.status()
	if err != nil {
		return err
	}
	if _, err := s.waitRelay(st, ""); err != nil {
		return err
	}
	if err := s.agents(); err != nil {
		return err
	}
	s.done()
	return nil
}
```

Modify `internal/cli/cmd_pair.go` (apply this change):

```diff
diff --git a/internal/cli/cmd_pair.go b/internal/cli/cmd_pair.go
index cbb9526..a76b3ba 100644
--- a/internal/cli/cmd_pair.go
+++ b/internal/cli/cmd_pair.go
@@ -49,17 +49,22 @@ func newJoinCmd(env *Env) *cobra.Command {
 			if err != nil {
 				return err
 			}
-			var pending ipc.PendingPeerResult
-			if err := withUnlock(ctx, env, c, func() error {
-				return c.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: code}, &pending)
-			}); err != nil {
-				return err
-			}
-			return finalizePeer(ctx, env, c, pending)
+			return joinNow(ctx, env, c, code)
 		},
 	}
 }
 
+// joinNow joins with a bind code and asks for the other machine's local name.
+func joinNow(ctx context.Context, env *Env, c Caller, code string) error {
+	var pending ipc.PendingPeerResult
+	if err := withUnlock(ctx, env, c, func() error {
+		return c.Call(ctx, ipc.MethodJoinStart, ipc.JoinStartParams{Code: code}, &pending)
+	}); err != nil {
+		return err
+	}
+	return finalizePeer(ctx, env, c, pending)
+}
+
 // pairNow creates a bind code, shows it as a join code (and QR code when qr
 // is set), waits for the other machine and asks for its local name.
 func pairNow(ctx context.Context, env *Env, c Caller, qr bool) error {
```

Modify `internal/cli/cmd_setup.go` (apply this change):

```diff
diff --git a/internal/cli/cmd_setup.go b/internal/cli/cmd_setup.go
index 2ac5a92..131682f 100644
--- a/internal/cli/cmd_setup.go
+++ b/internal/cli/cmd_setup.go
@@ -11,6 +11,7 @@ import (
 
 	"github.com/cookwithcravv/cravv-connect/internal/config"
 	"github.com/cookwithcravv/cravv-connect/internal/ipc"
+	"github.com/cookwithcravv/cravv-connect/internal/joincode"
 	"github.com/cookwithcravv/cravv-connect/internal/relayaddr"
 	"github.com/spf13/cobra"
 )
@@ -22,6 +23,7 @@ type setupOptions struct {
 	relay, token, name string
 	yes, noAgents      bool
 	pair, reset        bool
+	join               string
 }
 
 func newSetupCmd(env *Env) *cobra.Command {
@@ -46,6 +48,10 @@ func newSetupCmd(env *Env) *cobra.Command {
 	f.BoolVar(&o.noAgents, "no-agents", false, "do not set up agent integrations")
 	f.BoolVar(&o.pair, "pair", false, "pair a device at the end without asking")
 	f.BoolVar(&o.reset, "reset", false, "set this machine up again from the start (asks first)")
+	f.StringVar(&o.join, "join", "", "join the machine that showed this join code (cravv-join:...), on its relay")
+	cmd.MarkFlagsMutuallyExclusive("join", "relay")
+	cmd.MarkFlagsMutuallyExclusive("join", "relay-token")
+	cmd.MarkFlagsMutuallyExclusive("join", "pair")
 	return cmd
 }
 
@@ -69,6 +75,14 @@ func runSetup(ctx context.Context, env *Env, o setupOptions) error {
 		return err
 	}
 	s := &setup{ctx: ctx, env: env, sys: env.setupSystem(), o: o, w: env.Stdout, paths: paths}
+	var jc *joincode.Code
+	if o.join != "" {
+		c, err := setupJoinCode(o.join)
+		if err != nil {
+			return err
+		}
+		jc = &c
+	}
 	cfg, configured, err := s.current()
 	if err != nil {
 		return err
@@ -87,6 +101,9 @@ func runSetup(ctx context.Context, env *Env, o setupOptions) error {
 		}
 		configured = false
 	}
+	if jc != nil {
+		return s.joinFlow(*jc, cfg, configured)
+	}
 	return s.hostFlow(cfg, configured)
 }
 
@@ -237,6 +254,14 @@ func (s *setup) daemonOnline(install, needRelay bool) (ipc.StatusResult, error)
 	if err != nil || !needRelay {
 		return st, err
 	}
+	return s.waitRelay(st, "If another machine is already on this relay, set this one up with "+
+		"`cravv-connect setup --reset --join <code>` instead (a join code from `cravv-connect pair` there). ")
+}
+
+// waitRelay waits until the daemon is connected to the relay, polling from
+// st; on a timeout the error names the daemon's errors, advice and the logs.
+func (s *setup) waitRelay(st ipc.StatusResult, advice string) (ipc.StatusResult, error) {
+	var err error
 	if !st.RelayConnected {
 		fmt.Fprintln(s.w, "Waiting for the relay connection...")
 	}
@@ -247,9 +272,7 @@ func (s *setup) daemonOnline(install, needRelay bool) (ipc.StatusResult, error)
 			for _, e := range st.Errors {
 				msg += "; " + terminalSafe(e)
 			}
-			return st, fmt.Errorf("%s. If another machine is already on this relay, set this one up with "+
-				"`cravv-connect setup --reset --join <code>` instead (a join code from `cravv-connect pair` there). Logs: %s",
-				msg, daemonLogHint(s.env))
+			return st, fmt.Errorf("%s. %sLogs: %s", msg, advice, daemonLogHint(s.env))
 		}
 		select {
 		case <-s.ctx.Done():
```

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./internal/cli -run '^(TestSetupJoinNewMachine|TestSetupJoinDeclined|TestSetupJoinRelayRule|TestSetupJoinOnASetUpMachine|TestSetupResetJoin|TestSetupJoinInputs)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/cmd_pair.go internal/cli/cmd_setup.go internal/cli/setup_join.go internal/cli/setup_join_test.go
git commit -m "setup --join: join the relay in a join code after confirming it, then pair and set up the agents

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: release: tagged builds for four platforms with SHA256SUMS, CI, and cravv-connect version

A `v*` tag builds the release archives on native runners (cgo for PAM), writes `SHA256SUMS` and publishes a GitHub release. CI vets and tests on every push to main and pull request, including the Cloudflare relay. `cravv-connect version` prints the version stamped with `-ldflags`.

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `internal/cli/cmd_version.go`
- Modify: `Makefile`
- Test: `cmd/cravv-connect/release_test.go` (new), `internal/cli/version_test.go` (new)

**Interfaces:**

Consumes: the existing `cli.Version` variable (`internal/cli/cmd_mcp.go`, already documented for `-X github.com/cookwithcravv/cravv-connect/internal/cli.Version=...` and reported by the MCP server).

Produces:

```go
// internal/cli/cmd_version.go
func buildVersion() string // cli.Version, else the module version from `go install ...@vX`, else "dev"
// `cravv-connect version` prints "cravv-connect <version> (<goos>/<goarch>)"
```

Release archives: `cravv-connect_<version without v>_<os>_<arch>.tar.gz`, each holding a directory of the same name with `cravv-connect`, `cravv-relay`, `cravv-conformance` and `README.md`; `SHA256SUMS` in `sha256sum` format.

**Design notes:**
- The version variable already existed as `cli.Version`; the workflow and the Makefile stamp that one instead of adding a second variable.
- Runners: `macos-14` (darwin/arm64), `macos-13` (darwin/amd64), `ubuntu-24.04` (linux/amd64), `ubuntu-24.04-arm` (linux/arm64); Linux installs `libpam0g-dev`. Each job checks `go env GOOS/GOARCH` matches its matrix entry, runs the tests, builds, and checks `cravv-connect version` prints the tag.
- `cravv-relay` and `cravv-conformance` are built with `CGO_ENABLED=0`; everything with `-trimpath -ldflags "-s -w"`.
- The publish job needs `contents: write` (the build jobs only read) and uses `gh release create --verify-tag --generate-notes`.
- YAML was validated locally with `python3` and PyYAML 6.0.3 (parse, every job has `runs-on` and steps, every step has exactly one of `uses`/`run`); actionlint is not installed. The build step was dry-run locally for darwin/arm64 (archive layout, PAM linked, `version` check) and its archive was installed with `scripts/install.sh` from a local HTTP server.
- `TestReleaseWorkflowStampsTheVersion` extracts the `-X` flag from `release.yml`, builds with it and runs `version`, so a wrong variable path cannot silently ship "dev".

- [ ] **Step 1: Write the failing tests**

Create `cmd/cravv-connect/release_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func releaseWorkflow(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The release workflow builds the four platforms on native runners, the
// relay and conformance tools without cgo, and archives named the way
// scripts/install.sh expects.
func TestReleaseWorkflowShape(t *testing.T) {
	wf := releaseWorkflow(t)
	for _, want := range []string{
		"{ runner: macos-14, goos: darwin, goarch: arm64 }",
		"{ runner: macos-13, goos: darwin, goarch: amd64 }",
		"{ runner: ubuntu-24.04, goos: linux, goarch: amd64 }",
		"{ runner: ubuntu-24.04-arm, goos: linux, goarch: arm64 }",
		"libpam0g-dev",
		`CGO_ENABLED=1 go build`,
		`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$name/cravv-relay" ./cmd/cravv-relay`,
		`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$name/cravv-conformance" ./cmd/cravv-conformance`,
		`name="cravv-connect_${version}_${GOOS}_${GOARCH}"`,
		`version="${TAG#v}"`,
		`sha256sum *.tar.gz > SHA256SUMS`,
		`tags: ["v*"]`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml lacks %s", want)
		}
	}
}

// The workflow stamps the tag into the binary with -X; a wrong variable path
// would silently leave "dev". Build with the workflow's flag and ask.
func TestReleaseWorkflowStampsTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	m := regexp.MustCompile(`-X (\S+)=\$\{TAG\}`).FindStringSubmatch(releaseWorkflow(t))
	if m == nil {
		t.Fatal("release.yml does not stamp the version with -X <variable>=${TAG}")
	}
	bin := filepath.Join(t.TempDir(), "cravv-connect")
	build := exec.Command("go", "build", "-ldflags", "-X "+m[1]+"=v9.9.9-test", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "version").Output()
	if want := "cravv-connect v9.9.9-test (" + runtime.GOOS + "/" + runtime.GOARCH + ")\n"; err != nil || string(out) != want {
		t.Fatalf("version: %v %q, want %q", err, out, want)
	}
}
```

Create `internal/cli/version_test.go`:

```go
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
	if got := buildVersion(); got != "dev" {
		t.Fatalf("unstamped build reports %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./cmd/cravv-connect ./internal/cli -run '^(TestReleaseWorkflowShape|TestReleaseWorkflowStampsTheVersion|TestVersionCommand)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
--- FAIL: TestReleaseWorkflowShape (0.00s)
    release_test.go:26: open ../../.github/workflows/release.yml: no such file or directory
--- FAIL: TestReleaseWorkflowStampsTheVersion (0.00s)
    release_test.go:53: open ../../.github/workflows/release.yml: no such file or directory
FAIL
FAIL	github.com/cookwithcravv/cravv-connect/cmd/cravv-connect	0.599s
FAIL	github.com/cookwithcravv/cravv-connect/internal/cli [build failed]
FAIL
# github.com/cookwithcravv/cravv-connect/internal/cli [github.com/cookwithcravv/cravv-connect/internal/cli.test]
internal/cli/version_test.go:18:12: undefined: buildVersion
```

- [ ] **Step 3: Implement**

Create `.github/workflows/ci.yml`:

```yaml
# Vet and test on every push to main and every pull request: the Go module
# with cgo (PAM) on Linux and macOS, the no-cgo build, and the Cloudflare
# relay's typecheck and tests.
name: ci

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  go:
    name: go (${{ matrix.runner }})
    strategy:
      fail-fast: false
      matrix:
        runner: [ubuntu-24.04, macos-14]
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Install the PAM headers
        if: runner.os == 'Linux'
        run: sudo apt-get update && sudo apt-get install -y libpam0g-dev
      - name: gofmt
        run: test -z "$(gofmt -l internal e2e cmd)"
      - name: Vet
        run: go vet ./...
      - name: Test
        run: go test ./... -race -count=1
      - name: Build without cgo
        run: CGO_ENABLED=0 go build ./...

  relay-cf:
    runs-on: ubuntu-24.04
    defaults:
      run:
        working-directory: relay-cf
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 22
          cache: npm
          cache-dependency-path: relay-cf/package-lock.json
      - run: npm ci
      - run: npm run typecheck
      - run: npm test
```

Create `.github/workflows/release.yml`:

```yaml
# Builds the release archives when a v* tag is pushed and publishes them with
# SHA256SUMS on the GitHub release. cravv-connect needs cgo for PAM, so each
# platform builds on a native runner; cravv-relay and cravv-conformance are
# pure Go (CGO_ENABLED=0). scripts/install.sh downloads these archives.
name: release

on:
  push:
    tags: ["v*"]

permissions:
  contents: read

jobs:
  build:
    name: build ${{ matrix.goos }}/${{ matrix.goarch }}
    strategy:
      fail-fast: true
      matrix:
        include:
          - { runner: macos-14, goos: darwin, goarch: arm64 }
          - { runner: macos-13, goos: darwin, goarch: amd64 }
          - { runner: ubuntu-24.04, goos: linux, goarch: amd64 }
          - { runner: ubuntu-24.04-arm, goos: linux, goarch: arm64 }
    runs-on: ${{ matrix.runner }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Install the PAM headers
        if: runner.os == 'Linux'
        run: sudo apt-get update && sudo apt-get install -y libpam0g-dev
      - name: Check the runner builds for its own platform
        run: test "$(go env GOOS)/$(go env GOARCH)" = "${{ matrix.goos }}/${{ matrix.goarch }}"
      - name: Test
        run: go test ./... -count=1
      - name: Build the archive
        env:
          TAG: ${{ github.ref_name }}
          GOOS: ${{ matrix.goos }}
          GOARCH: ${{ matrix.goarch }}
        run: |
          set -eu
          version="${TAG#v}"
          name="cravv-connect_${version}_${GOOS}_${GOARCH}"
          mkdir -p "dist/$name"
          CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X github.com/cookwithcravv/cravv-connect/internal/cli.Version=${TAG}" \
            -o "dist/$name/cravv-connect" ./cmd/cravv-connect
          CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$name/cravv-relay" ./cmd/cravv-relay
          CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$name/cravv-conformance" ./cmd/cravv-conformance
          cp README.md "dist/$name/"
          "dist/$name/cravv-connect" version | grep -F "cravv-connect ${TAG} (${GOOS}/${GOARCH})"
          tar -C dist -czf "dist/$name.tar.gz" "$name"
      - uses: actions/upload-artifact@v4
        with:
          name: cravv-connect-${{ matrix.goos }}-${{ matrix.goarch }}
          path: dist/*.tar.gz
          if-no-files-found: error

  publish:
    needs: build
    runs-on: ubuntu-24.04
    permissions:
      contents: write
    steps:
      - uses: actions/download-artifact@v4
        with:
          path: dist
          merge-multiple: true
      - name: Write SHA256SUMS
        working-directory: dist
        run: |
          set -eu
          test "$(ls *.tar.gz | wc -l)" -eq 4
          sha256sum *.tar.gz > SHA256SUMS
          cat SHA256SUMS
      - name: Publish the release
        env:
          GH_TOKEN: ${{ github.token }}
          TAG: ${{ github.ref_name }}
        run: gh release create "$TAG" dist/*.tar.gz dist/SHA256SUMS --repo "$GITHUB_REPOSITORY" --title "$TAG" --generate-notes --verify-tag
```

Create `internal/cli/cmd_version.go`:

```go
package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func init() { Register(newVersionCmd) }

// buildVersion is Version as stamped by the release build, or the module
// version for `go install ...@vX.Y.Z`, or "dev".
func buildVersion() string {
	if Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return Version
}

func newVersionCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of this binary",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintf(env.Stdout, "cravv-connect %s (%s/%s)\n", buildVersion(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
```

Modify `Makefile` (apply this change):

```diff
diff --git a/Makefile b/Makefile
index f4bc433..a6b32b0 100644
--- a/Makefile
+++ b/Makefile
@@ -1,11 +1,12 @@
 .PHONY: all build test vet relay-test clean
 
 GO ?= go
+VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
 
 all: vet test build
 
 build:
-	$(GO) build -o bin/cravv-connect ./cmd/cravv-connect
+	$(GO) build -ldflags "-X github.com/cookwithcravv/cravv-connect/internal/cli.Version=$(VERSION)" -o bin/cravv-connect ./cmd/cravv-connect
 	CGO_ENABLED=0 $(GO) build -o bin/cravv-relay ./cmd/cravv-relay
 	CGO_ENABLED=0 $(GO) build -o bin/cravv-conformance ./cmd/cravv-conformance
 
```

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./cmd/cravv-connect ./internal/cli -run '^(TestReleaseWorkflowShape|TestReleaseWorkflowStampsTheVersion|TestVersionCommand)$' -count=1 -race
gofmt -l internal e2e cmd
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml Makefile cmd/cravv-connect/release_test.go internal/cli/cmd_version.go internal/cli/version_test.go
git commit -m "release: tagged builds for four platforms with SHA256SUMS, CI, and cravv-connect version

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 7: install.sh: download a release, check it against SHA256SUMS, install to ~/.local/bin or with --system

`scripts/install.sh` is the install one-liner: POSIX `sh` with `set -eu`, it detects the OS and architecture, finds the latest release (or `$CRAVV_VERSION`), downloads the archive and `SHA256SUMS`, verifies the archive, and installs `cravv-connect` and `cravv-relay` to `~/.local/bin` (warning when that is not on `PATH`) or `/usr/local/bin` with `--system` (sudo when needed). A Go test harness serves a fake GitHub release over `httptest` and runs the script with `sh` (and `dash` when installed).

**Files:**
- Create: `scripts/install.sh`
- Test: `scripts/install_test.go` (new)

**Interfaces:**

Consumes: the release layout from Task 6.

Produces: `scripts/install.sh [--system]` with the environment `CRAVV_VERSION`, `CRAVV_REPO` (default `cravv/cravv-connect`, a placeholder with a comment to update it once the real repository exists), `CRAVV_BASE_URL` (default `https://github.com/$CRAVV_REPO/releases`; serves `<base>/latest` and `<base>/download/<tag>/<file>`), and the test hook `CRAVV_SYSTEM_DIR` (default `/usr/local/bin`).

**Design notes:**
- Latest release: the URL that `<base>/latest` redirects to ends in `/tag/<tag>` (curl `%{url_effective}`, or the last `Location:` header with wget).
- The tag must look like `v<digit>...` and contain only `[A-Za-z0-9.+-]`.
- Checksums with `sha256sum` or `shasum -a 256`; a mismatch or a missing entry installs nothing.
- Rosetta: a shell translated on Apple silicon reports x86_64, so `sysctl.proc_translated` switches it back to arm64.
- Binaries are copied to `.<name>.new` and renamed over the old ones, so a running daemon keeps its file; after an upgrade the script says to restart the daemon.
- `cravv-conformance` is not installed (a developer tool; it stays in the archive).

- [ ] **Step 1: Write the failing tests**

Create `scripts/install_test.go`:

```go
// Package scripts holds the tests of scripts/install.sh: a fake release
// server and the script run with sh (and dash when installed).
package scripts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeRelease serves <base>/latest (a redirect to <base>/tag/<latest>) and
// <base>/download/<tag>/<file> for each tag, like GitHub releases.
type fakeRelease struct {
	latest string
	mu     sync.Mutex
	files  map[string][]byte // "<tag>/<file>"
	srv    *httptest.Server
}

func (r *fakeRelease) file(k string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.files[k]
	return b, ok
}

func (r *fakeRelease) set(k string, b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[k] = b
}

func archiveName(tag string) string {
	return fmt.Sprintf("cravv-connect_%s_%s_%s", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

// tarball builds the release archive: a directory named like the archive
// holding fake binaries that print their name and tag.
func tarball(t *testing.T, tag string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dir := archiveName(tag)
	tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, b := range []string{"cravv-connect", "cravv-relay", "cravv-conformance", "README.md"} {
		body := []byte("#!/bin/sh\necho " + b + " " + tag + "\n")
		if err := tw.WriteHeader(&tar.Header{Name: dir + "/" + b, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

func newFakeRelease(t *testing.T, latest string, tags ...string) *fakeRelease {
	r := &fakeRelease{latest: latest, files: map[string][]byte{}}
	for _, tag := range tags {
		tgz := tarball(t, tag)
		sum := sha256.Sum256(tgz)
		name := archiveName(tag) + ".tar.gz"
		r.files[tag+"/"+name] = tgz
		r.files[tag+"/SHA256SUMS"] = []byte(fmt.Sprintf("%s  %s\n%s  cravv-connect_other_linux_s390x.tar.gz\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64)))
	}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch p := req.URL.Path; {
		case p == "/releases/latest":
			http.Redirect(w, req, "/releases/tag/"+r.latest, http.StatusFound)
		case strings.HasPrefix(p, "/releases/tag/"):
			w.Write([]byte("<html>release</html>"))
		case strings.HasPrefix(p, "/releases/download/"):
			b, ok := r.file(strings.TrimPrefix(p, "/releases/download/"))
			if !ok {
				http.NotFound(w, req)
				return
			}
			w.Write(b)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// shells are the POSIX shells the script is run with.
func shells(t *testing.T) []string {
	out := []string{"sh"}
	if _, err := exec.LookPath("dash"); err == nil {
		out = append(out, "dash")
	}
	return out
}

type result struct {
	code           int
	stdout, stderr string
	home           string
}

// install runs scripts/install.sh with shell in a fresh HOME against r.
func install(t *testing.T, shell string, r *fakeRelease, env []string, args ...string) result {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(shell, append([]string{"install.sh"}, args...)...)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CRAVV_BASE_URL=" + r.srv.URL + "/releases"}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{code, out.String(), errb.String(), home}
}

func ran(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("%s: %v", bin, err)
	}
	return string(out)
}

// With no version the latest release is installed to ~/.local/bin, both
// binaries executable, with a hint when that folder is not on PATH.
func TestInstallLatest(t *testing.T) {
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	for _, sh := range shells(t) {
		res := install(t, sh, r, nil)
		if res.code != 0 {
			t.Fatalf("%s: code %d stderr %s", sh, res.code, res.stderr)
		}
		bin := filepath.Join(res.home, ".local", "bin")
		if got := ran(t, filepath.Join(bin, "cravv-connect")); got != "cravv-connect v1.2.3\n" {
			t.Fatalf("%s: cravv-connect says %q", sh, got)
		}
		if got := ran(t, filepath.Join(bin, "cravv-relay")); got != "cravv-relay v1.2.3\n" {
			t.Fatalf("%s: cravv-relay says %q", sh, got)
		}
		if _, err := os.Stat(filepath.Join(bin, "cravv-conformance")); err == nil {
			t.Fatalf("%s: installed the conformance tool", sh)
		}
		for _, want := range []string{
			"Downloading cravv-connect v1.2.3 for " + runtime.GOOS + "/" + runtime.GOARCH + "...\n",
			"Installed cravv-connect v1.2.3 and cravv-relay to " + bin + ".\n",
			bin + " is not on your PATH.",
			"Next, on the first machine:  cravv-connect setup\n",
		} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("%s: stdout lacks %q:\n%s", sh, want, res.stdout)
			}
		}
	}
}

// CRAVV_VERSION pins a release; installing over an existing binary says to
// restart the daemon; a folder on PATH gets no hint.
func TestInstallPinnedVersionOverAnOldOne(t *testing.T) {
	r := newFakeRelease(t, "v1.2.3", "v1.2.3", "v1.0.0")
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "cravv-connect"), []byte("old"), 0o755)
	cmd := exec.Command("sh", "install.sh")
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH"), "CRAVV_BASE_URL=" + r.srv.URL + "/releases", "CRAVV_VERSION=v1.0.0"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := ran(t, filepath.Join(bin, "cravv-connect")); got != "cravv-connect v1.0.0\n" {
		t.Fatalf("cravv-connect says %q", got)
	}
	if strings.Contains(string(out), "is not on your PATH") || !strings.Contains(string(out), "cravv-connect daemon stop && cravv-connect daemon start") {
		t.Fatalf("output\n%s", out)
	}
}

// A download that does not match SHA256SUMS, or is missing from it, installs
// nothing.
func TestInstallChecksTheDownload(t *testing.T) {
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	name := archiveName("v1.2.3") + ".tar.gz"
	good, _ := r.file("v1.2.3/" + name)
	r.set("v1.2.3/"+name, append(append([]byte(nil), good...), 0))
	res := install(t, "sh", r, nil)
	if res.code == 0 || !strings.Contains(res.stderr, "install.sh: checksum mismatch for "+name) {
		t.Fatalf("tampered: code %d stderr %q", res.code, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(res.home, ".local", "bin", "cravv-connect")); err == nil {
		t.Fatal("installed a tampered archive")
	}
	r.set("v1.2.3/"+name, good)
	r.set("v1.2.3/SHA256SUMS", []byte(strings.Repeat("0", 64)+"  cravv-connect_other_linux_s390x.tar.gz\n"))
	if res := install(t, "sh", r, nil); res.code == 0 || !strings.Contains(res.stderr, "install.sh: SHA256SUMS has no entry for "+name) {
		t.Fatalf("no entry: code %d stderr %q", res.code, res.stderr)
	}
}

func TestInstallBadInput(t *testing.T) {
	r := newFakeRelease(t, "latest-junk", "v1.2.3")
	if res := install(t, "sh", r, nil); res.code == 0 || !strings.Contains(res.stderr, "not a release tag: latest-junk") {
		t.Fatalf("bad latest tag: code %d stderr %q", res.code, res.stderr)
	}
	if res := install(t, "sh", r, []string{"CRAVV_VERSION=v1;rm"}); res.code == 0 || !strings.Contains(res.stderr, "not a release tag: v1;rm") {
		t.Fatalf("bad pinned tag: code %d stderr %q", res.code, res.stderr)
	}
	if res := install(t, "sh", r, []string{"CRAVV_VERSION=v9.9.9"}); res.code == 0 || !strings.Contains(res.stderr, "download failed: "+r.srv.URL+"/releases/download/v9.9.9/") {
		t.Fatalf("missing release: code %d stderr %q", res.code, res.stderr)
	}
	if res := install(t, "sh", r, nil, "--bogus"); res.code != 2 || !strings.Contains(res.stderr, "usage: install.sh [--system]") {
		t.Fatalf("bad option: code %d stderr %q", res.code, res.stderr)
	}
}

// --system installs to /usr/local/bin (here a stand-in folder the user cannot
// write) through sudo.
func TestInstallSystemUsesSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	sys := t.TempDir()
	os.Chmod(sys, 0o555)
	t.Cleanup(func() { os.Chmod(sys, 0o755) })
	fake := t.TempDir()
	sudoLog := filepath.Join(fake, "sudo.log")
	os.WriteFile(filepath.Join(fake, "sudo"), []byte("#!/bin/sh\necho \"$*\" >> "+sudoLog+"\nchmod u+w "+sys+"\nexec \"$@\"\n"), 0o755)
	res := install(t, "sh", r, []string{"CRAVV_SYSTEM_DIR=" + sys, "PATH=" + fake + ":" + os.Getenv("PATH")}, "--system")
	if res.code != 0 {
		t.Fatalf("code %d stderr %s", res.code, res.stderr)
	}
	if got := ran(t, filepath.Join(sys, "cravv-connect")); got != "cravv-connect v1.2.3\n" {
		t.Fatalf("cravv-connect says %q", got)
	}
	log, _ := os.ReadFile(sudoLog)
	if !strings.Contains(string(log), "mv -f "+sys+"/.cravv-connect.new "+sys+"/cravv-connect") || !strings.Contains(res.stdout, "Installing to "+sys+" needs sudo.") {
		t.Fatalf("sudo log %q stdout %s", log, res.stdout)
	}
	if _, err := os.Stat(filepath.Join(res.home, ".local", "bin")); err == nil {
		t.Fatal("--system also installed to ~/.local/bin")
	}
}

// Without CRAVV_BASE_URL the script asks GitHub for CRAVV_REPO's latest
// release (default cravv/cravv-connect). A fake curl records the request.
func TestInstallDefaultRepository(t *testing.T) {
	fake := t.TempDir()
	log := filepath.Join(fake, "curl.log")
	os.WriteFile(filepath.Join(fake, "curl"), []byte("#!/bin/sh\necho \"$*\" >> "+log+"\nexit 22\n"), 0o755)
	for repo, want := range map[string]string{"": "https://github.com/cookwithcravv/cravv-connect/releases/latest", "acme/cc": "https://github.com/acme/cc/releases/latest"} {
		os.Remove(log)
		cmd := exec.Command("sh", "install.sh")
		cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + fake + ":" + os.Getenv("PATH")}
		if repo != "" {
			cmd.Env = append(cmd.Env, "CRAVV_REPO="+repo)
		}
		out, err := cmd.CombinedOutput()
		got, _ := os.ReadFile(log)
		if err == nil || !strings.Contains(string(got), want) || !strings.Contains(string(out), "cannot find the latest release at "+strings.TrimSuffix(want, "/latest")+"/latest") {
			t.Fatalf("repo %q: %v\n%s\ncurl: %s", repo, err, out, got)
		}
	}
}

// Without curl the script uses wget (for the latest release too). PATH holds
// only the tools the script needs, minus curl.
func TestInstallWithWget(t *testing.T) {
	if _, err := exec.LookPath("wget"); err != nil {
		t.Skip("wget is not installed")
	}
	tools := t.TempDir()
	for _, tool := range []string{"wget", "uname", "sed", "awk", "tar", "gzip", "mktemp", "rm", "cp", "chmod", "mv", "mkdir", "tr", "tail", "sha256sum", "shasum", "perl", "sysctl", "cat"} {
		if p, err := exec.LookPath(tool); err == nil {
			os.Symlink(p, filepath.Join(tools, tool))
		}
	}
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	res := install(t, "sh", r, []string{"PATH=" + tools})
	if res.code != 0 {
		t.Fatalf("code %d stderr %s", res.code, res.stderr)
	}
	if got := ran(t, filepath.Join(res.home, ".local", "bin", "cravv-connect")); got != "cravv-connect v1.2.3\n" {
		t.Fatalf("cravv-connect says %q", got)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./scripts -run '^(TestInstallLatest|TestInstallPinnedVersionOverAnOldOne|TestInstallChecksTheDownload|TestInstallBadInput|TestInstallSystemUsesSudo|TestInstallDefaultRepository|TestInstallWithWget)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
--- FAIL: TestInstallLatest (0.01s)
    install_test.go:151: sh: code 127 stderr sh: install.sh: No such file or directory
--- FAIL: TestInstallPinnedVersionOverAnOldOne (0.01s)
    install_test.go:188: exit status 127
        sh: install.sh: No such file or directory
--- FAIL: TestInstallChecksTheDownload (0.01s)
    install_test.go:207: tampered: code 127 stderr "sh: install.sh: No such file or directory\n"
--- FAIL: TestInstallBadInput (0.01s)
    install_test.go:222: bad latest tag: code 127 stderr "sh: install.sh: No such file or directory\n"
--- FAIL: TestInstallSystemUsesSudo (0.01s)
    install_test.go:250: code 127 stderr sh: install.sh: No such file or directory
--- FAIL: TestInstallDefaultRepository (0.01s)
    install_test.go:280: repo "": exit status 127
        sh: install.sh: No such file or directory
...
```

- [ ] **Step 3: Implement**

Create `scripts/install.sh`:

```sh
#!/bin/sh
# Install cravv-connect and cravv-relay from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/cravv/cravv-connect/main/scripts/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/cravv/cravv-connect/main/scripts/install.sh | sh -s -- --system
#
# Installs to ~/.local/bin, or /usr/local/bin with --system (using sudo when
# needed). The archive is checked against the release's SHA256SUMS before
# anything is installed.
#
# Environment:
#   CRAVV_VERSION   release tag to install, for example v1.2.0 (default: the latest release)
#   CRAVV_REPO      GitHub repository, owner/name (default: cravv/cravv-connect)
#   CRAVV_BASE_URL  release URL, default https://github.com/$CRAVV_REPO/releases
#                   (it serves <base>/latest and <base>/download/<tag>/<file>)
set -eu

# The default repository is a placeholder: update it (here and in the URLs
# above) once the real repository exists.
repo="${CRAVV_REPO:-cravv/cravv-connect}"
base="${CRAVV_BASE_URL:-https://github.com/$repo/releases}"
bindir="$HOME/.local/bin"
system=0

say() { printf '%s\n' "$*"; }
die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

usage() {
	say "usage: install.sh [--system]"
	say "  --system  install to /usr/local/bin instead of ~/.local/bin"
	say "Environment: CRAVV_VERSION (default: latest), CRAVV_REPO (default: cravv/cravv-connect), CRAVV_BASE_URL"
}

while [ $# -gt 0 ]; do
	case "$1" in
	--system) system=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		usage >&2
		exit 2
		;;
	esac
	shift
done
if [ "$system" = 1 ]; then
	bindir="${CRAVV_SYSTEM_DIR:-/usr/local/bin}"
fi

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) die "unsupported operating system $(uname -s) (releases exist for macOS and Linux)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported processor $(uname -m) (releases exist for amd64 and arm64)" ;;
esac
# A shell running under Rosetta reports x86_64 on Apple silicon.
if [ "$os" = darwin ] && [ "$arch" = amd64 ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
	arch=arm64
fi

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL -o "$2" "$1"; }
	# The URL that <base>/latest redirects to ends in /tag/<tag>.
	latest_url() { curl -fsSL -o /dev/null -w '%{url_effective}' "$base/latest"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
	latest_url() { wget -q -O /dev/null --server-response "$base/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r'; }
else
	die "curl or wget is needed"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
else
	die "sha256sum or shasum is needed to check the download"
fi

tag="${CRAVV_VERSION:-}"
if [ -z "$tag" ]; then
	url="$(latest_url)" || die "cannot find the latest release at $base/latest"
	tag="${url##*/tag/}"
	[ "$tag" != "$url" ] || die "cannot find the latest release at $base/latest (no release yet?)"
fi
case "$tag" in
v[0-9]*) ;;
*) die "not a release tag: $tag (expected something like v1.2.0)" ;;
esac
case "$tag" in
*[!A-Za-z0-9.+-]*) die "not a release tag: $tag" ;;
esac

name="cravv-connect_${tag#v}_${os}_${arch}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' HUP INT TERM

say "Downloading cravv-connect $tag for $os/$arch..."
fetch "$base/download/$tag/$name.tar.gz" "$tmp/$name.tar.gz" || die "download failed: $base/download/$tag/$name.tar.gz"
fetch "$base/download/$tag/SHA256SUMS" "$tmp/SHA256SUMS" || die "download failed: $base/download/$tag/SHA256SUMS"

want="$(awk -v f="$name.tar.gz" '$2 == f || $2 == "*" f {print $1}' "$tmp/SHA256SUMS")"
[ -n "$want" ] || die "SHA256SUMS has no entry for $name.tar.gz"
got="$(sha256 "$tmp/$name.tar.gz")"
[ "$got" = "$want" ] || die "checksum mismatch for $name.tar.gz (expected $want, got $got); nothing was installed"

tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
for b in cravv-connect cravv-relay; do
	[ -f "$tmp/$name/$b" ] || die "the archive has no $b"
done

sudo=""
mkdir -p "$bindir" 2>/dev/null || true
if [ ! -d "$bindir" ] || [ ! -w "$bindir" ]; then
	[ "$system" = 1 ] || die "cannot write to $bindir"
	command -v sudo >/dev/null 2>&1 || die "cannot write to $bindir and sudo is not available"
	sudo="sudo"
	say "Installing to $bindir needs sudo."
	$sudo mkdir -p "$bindir"
fi
upgrade=0
[ -e "$bindir/cravv-connect" ] && upgrade=1
for b in cravv-connect cravv-relay; do
	# Copy then rename, so a running daemon keeps its old file.
	$sudo cp "$tmp/$name/$b" "$bindir/.$b.new"
	$sudo chmod 0755 "$bindir/.$b.new"
	$sudo mv -f "$bindir/.$b.new" "$bindir/$b"
done

say "Installed cravv-connect $tag and cravv-relay to $bindir."
case ":$PATH:" in
*":$bindir:"*) ;;
*)
	say ""
	say "$bindir is not on your PATH. Add it, for example in ~/.profile or ~/.zshrc:"
	say "  export PATH=\"$bindir:\$PATH\""
	;;
esac
say ""
if [ "$upgrade" = 1 ]; then
	say "If the daemon is running, restart it to use the new version:"
	say "  cravv-connect daemon stop && cravv-connect daemon start"
else
	say "Next, on the first machine:  cravv-connect setup"
	say "On every other machine:      cravv-connect setup --join <code>"
fi
```

Make it executable: `chmod +x scripts/install.sh`.

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./scripts -run '^(TestInstallLatest|TestInstallPinnedVersionOverAnOldOne|TestInstallChecksTheDownload|TestInstallBadInput|TestInstallSystemUsesSudo|TestInstallDefaultRepository|TestInstallWithWget)$' -count=1 -race
gofmt -l internal e2e cmd scripts
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add scripts/install.sh scripts/install_test.go
git commit -m "install.sh: download a release, check it against SHA256SUMS, install to ~/.local/bin or with --system

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 8: README: quick start with the install one-liner, setup and setup --join

The README's quick start becomes: install with the one-liner, `cravv-connect setup` on the first machine, `cravv-connect setup --join <code>` on every other machine, then `/cravv` in Claude Code. The previous manual steps stay as "Relays" and "Manual setup" sections, and the CLI reference gains `setup`, `setup --join` and `version`.

**Files:**
- Modify: `README.md`
- Test: `scripts/readme_test.go` (new)

**Interfaces:**

Consumes: the install one-liner documented at the top of `scripts/install.sh`.

Produces: README sections `## Install` (one-liner, then `### Install from source`), `## Quick start` (`### 1. Set up the first machine`, `### 2. Set up every other machine`, `### 3. Use it from Claude Code`, `### Relays`, `### Manual setup: ...`).

**Design notes:**
- Only the install and quick start sections and three CLI reference rows change; the other sections (including the v1 trust wording elsewhere, which Phase 6 rewrites) are kept as they are.
- The manual pair example drops the removed trust-level prompt and shows the join code.
- `TestReadmeQuickStart` keeps the README one-liner equal to the one `install.sh` documents and checks there is no em dash.

- [ ] **Step 1: Write the failing tests**

Create `scripts/readme_test.go`:

```go
package scripts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The README's quick start installs with the same one-liner install.sh
// documents, then uses setup and setup --join; it has no em dashes.
func TestReadmeQuickStart(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	oneLiner := regexp.MustCompile(`(?m)^#   (curl -fsSL \S+/scripts/install\.sh \| sh)$`).FindSubmatch(script)
	if oneLiner == nil {
		t.Fatal("install.sh documents no one-liner")
	}
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	quick := string(readme)
	for _, want := range []string{string(oneLiner[1]), "\ncravv-connect setup\n", "\ncravv-connect setup --join cravv-join:", "type `/cravv`"} {
		if !strings.Contains(quick, want) {
			t.Errorf("README lacks %q", want)
		}
	}
	if strings.Contains(quick, "\u2014") {
		t.Error("README has an em dash")
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

```bash
go test ./scripts -run '^(TestReadmeQuickStart)$' -count=1
```

Expected: FAIL, with output starting like this (paths relative to the repository):

```
--- FAIL: TestReadmeQuickStart (0.00s)
    readme_test.go:28: README lacks "curl -fsSL https://raw.githubusercontent.com/cravv/cravv-connect/main/scripts/install.sh | sh"
    readme_test.go:28: README lacks "\ncravv-connect setup\n"
    readme_test.go:28: README lacks "\ncravv-connect setup --join cravv-join:"
    readme_test.go:28: README lacks "type `/cravv`"
FAIL
FAIL	github.com/cookwithcravv/cravv-connect/scripts	0.539s
FAIL
```

- [ ] **Step 3: Implement**

Modify `README.md` (apply this change):

````diff
diff --git a/README.md b/README.md
index bbc723e..19c02e2 100644
--- a/README.md
+++ b/README.md
@@ -42,7 +42,22 @@ reference relay `cravv-relay` for local testing. Protocols:
 [relay-v1](protocol/relay-v1.md), [peer-v1](protocol/peer-v1.md),
 [ipc-v1](protocol/ipc-v1.md).
 
-## Install from source
+## Install
+
+On macOS or Linux (arm64 or amd64):
+
+```sh
+curl -fsSL https://raw.githubusercontent.com/cravv/cravv-connect/main/scripts/install.sh | sh
+```
+
+This downloads the latest release, checks it against the release's
+`SHA256SUMS`, and installs `cravv-connect` and `cravv-relay` to
+`~/.local/bin` (it tells you if that folder is not on your `PATH`). No Go
+toolchain is needed. Use `sh -s -- --system` to install to `/usr/local/bin`
+instead, and `CRAVV_VERSION=v1.2.0` to pick a release. The repository URL is a
+placeholder until the project is published.
+
+### Install from source
 
 You need Go 1.26 and a C toolchain, because password checks use PAM through
 cgo:
@@ -65,7 +80,62 @@ check, so the binary the daemon runs is the one that needs cgo.
 
 ## Quick start
 
-### 1. Get a relay
+### 1. Set up the first machine
+
+```sh
+cravv-connect setup
+```
+
+The wizard walks through:
+
+1. **Relay.** Enter your relay URL (for real use, deploy the Cloudflare relay
+   once: [relay-cf/README.md](relay-cf/README.md)), or press Enter to start a
+   LAN test relay on this machine. The test relay is `cravv-relay` from next
+   to `cravv-connect`, on port 8787 at `http://<host>.local:8787` when that
+   name resolves (otherwise the machine's network address). It keeps
+   everything in memory and stops when the machine restarts.
+2. **Admin token**, only if this is the relay's first machine (setup makes
+   one for a test relay it starts).
+3. **This machine and the daemon.** It writes `~/.cravv-connect/config.toml`,
+   installs the daemon as a login service (launchd on macOS, systemd on
+   Linux) and waits until it is connected to the relay.
+4. **Agents.** It detects Claude Code and Codex and offers to add
+   cravv-connect to each.
+5. **Pair a device.** It shows a join code and its QR code, and waits for
+   the other machine.
+
+Run `cravv-connect setup` again at any time: it shows what is set up and
+offers only the missing steps. `cravv-connect setup --reset` starts over (it
+asks first). Without questions:
+`cravv-connect setup --yes --relay <url> [--relay-token <token>] [--name <name>] [--no-agents]`.
+
+### 2. Set up every other machine
+
+Install as above, then use the join code the first machine shows:
+
+```sh
+cravv-connect setup --join cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A
+```
+
+It shows the relay in the code and asks `Join relay https://relay.example.com? (y/N)`.
+It refuses a plain `http` relay unless the address is this machine or a
+private network, and it refuses a machine already set up for another relay
+(`cravv-connect setup --reset --join <code>` moves it; you pair again with
+every peer). Then it sets the machine up, joins (your login password), asks
+for a local name for the other machine, and offers the agents.
+
+A join code works once and expires in 10 minutes. To pair two machines that
+are already set up, run `cravv-connect pair` on one and
+`cravv-connect join <code>` on the other (the join code or the plain bind
+code).
+
+### 3. Use it from Claude Code
+
+Restart Claude Code and type `/cravv` in a chat. The chat is shared as a
+session, and sessions on paired machines can ask to link with it; you decide
+each link.
+
+### Relays
 
 For real use, deploy the Cloudflare relay once: follow
 [relay-cf/README.md](relay-cf/README.md). You end up with a URL such as
@@ -84,7 +154,7 @@ they sign (both the WebSocket login and every blob request cover the
 normalized origin). The admin token can also come from
 `CRAVV_RELAY_ADMIN_TOKEN`.
 
-### 2. Set up the first machine
+### Manual setup: the first machine
 
 ```sh
 cravv-connect init --relay https://cravv-relay.example.workers.dev --relay-token <admin token>
@@ -97,7 +167,7 @@ deleted. Use `--relay-token -` to read it from stdin, and `--name` to choose
 the device name peers see as a suggestion (default: the host name up to the
 first `.`; lowercased to letters, digits and dashes, at most 24 characters).
 
-### 3. Set up the other machine
+### Manual setup: the other machine
 
 ```sh
 cravv-connect init --relay https://cravv-relay.example.workers.dev
@@ -113,14 +183,15 @@ resolved, so install a built binary (for example `make build`, then
 temporary directory or Go's build cache. It then waits up to 10 seconds for
 the daemon to answer and, if it does not, says where the logs are.
 
-### 4. Pair
+### Manual setup: pair
 
 On the first machine:
 
 ```sh
 cravv-connect pair
 # Login password for this machine:
-# Bind code: CRAVV-7K3F-9QXM-TR2A
+# Join code: cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A
+# (a QR code of it, and the plain bind code CRAVV-7K3F-9QXM-TR2A)
 ```
 
 Share the code any way you like (it works once and expires in 10 minutes). On
@@ -130,11 +201,11 @@ the other machine:
 cravv-connect join CRAVV-7K3F-9QXM-TR2A
 ```
 
-Both sides ask for the login password, then for a local name for the peer and
-a trust level (default `ask-first`). `cravv-connect peers` lists paired
-machines with their machine IDs, so you can compare them later.
+Both sides ask for the login password, then for a local name for the peer.
+`cravv-connect peers` lists paired machines with their machine IDs, so you can
+compare them later.
 
-### 5. Connect your agents
+### Manual setup: connect your agents
 
 ```sh
 cravv-connect install claude      # MCP server plus UserPromptSubmit and Stop hooks
@@ -220,13 +291,16 @@ switch is on, only `resume` (and `status`, `peers`, `log`, `kill` again, and
 
 | Command | What it does |
 |---|---|
+| `setup [--relay <url>] [--relay-token <t>] [--name <n>] [--yes] [--no-agents] [--pair] [--reset]` | Guided setup: relay (or a LAN test relay), init, the daemon, agents, pairing. Safe to run again |
+| `setup --join <join code> [--name <n>] [--yes] [--no-agents] [--reset]` | Set this machine up on the relay in a join code and pair with the machine that showed it |
+| `version` | Print the version |
 | `init --relay <url> [--relay-token <t>] [--name <n>] [--force]` | Write `config.toml`; store the admin token for the first machine. The relay URL must be an origin, `scheme://host[:port]`, with no path. `--force` with a different relay clears this machine's relay registration so it registers again there; peers are not told, so re-pair with them |
 | `daemon run [--log-file <path>]` | Run the daemon in the foreground. Logs JSON to stderr, or with `--log-file` to that file, rotated at 10 MiB with 3 old files kept |
 | `daemon start` / `daemon stop` / `daemon status` | Control the daemon. `stop` asks the daemon over its socket to shut down and waits up to 10 seconds for it to exit; it never signals a process that does not answer on the socket |
 | `daemon install` / `daemon uninstall` | Run the daemon at login (launchd or systemd user unit) |
 | `status [--json]` | Relay connection, peers, queues, pending approvals, sessions, errors |
-| `pair` | Create a bind code and pair (password) |
-| `join <code>` | Join with a bind code (password) |
+| `pair [--no-qr]` | Show a join code (with a QR code) and pair (password) |
+| `join <code>` | Join with a join code for this machine's relay, or a bind code (password) |
 | `peers` | List peers with trust, state and machine ID |
 | `alias <alias> <new-alias>` | Rename a peer locally |
 | `trust <alias> <chat-only\|ask-first\|autonomous>` | Set a peer's trust (raising needs the password) |
````

- [ ] **Step 4: Run the tests to see them pass, then everything**

```bash
go test ./scripts -run '^(TestReadmeQuickStart)$' -count=1 -race
gofmt -l internal e2e cmd scripts
go vet ./...
go test ./... -race -count=1
```

Expected: the task's tests PASS, `gofmt -l` prints nothing, `go vet` is clean, and every package reports `ok` (e2e included).

- [ ] **Step 5: Commit**

```bash
git add README.md scripts/readme_test.go
git commit -m "README: quick start with the install one-liner, setup and setup --join

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Manual check after Task 8

These need real machines and a real login service, which the tests replace with fakes.

1. On a Mac with a build from the release workflow (or `make build`), run `cravv-connect setup`, press Enter at the relay prompt, and check: the relay URL is `http://<host>.local:8787`, `ps -o command= -p $(pgrep -x cravv-relay)` shows no token, `cravv-connect status` shows the relay connected, and "Pair a device now?" prints a QR code your phone camera reads as the join code.
2. On a Linux box on the same network, run the install one-liner (with `CRAVV_BASE_URL` pointing at a test release if the repository is not public yet), then `cravv-connect setup --join <code>`: it asks "Join relay http://<host>.local:8787? (y/N)", then the password and the alias; `cravv-connect peers` lists the Mac on both sides.
3. Run `cravv-connect setup` again on both: each shows its relay and offers only missing steps. Run `cravv-connect setup --relay https://example.com` on one: it refuses with the `--reset` advice.
4. Push a `v0.0.1-rc1` tag to a fork: the release has four archives and `SHA256SUMS`, and `sha256sum -c SHA256SUMS` passes after downloading them.
