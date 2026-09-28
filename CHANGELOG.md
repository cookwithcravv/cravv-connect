# Changelog

All notable changes to cravv-connect. Versions follow
[semantic versioning](https://semver.org); before 1.0, a minor version may
change behaviour or wire formats, and the notes say so.

## Unreleased

### Added

- `relay-cf/scripts/deploy.sh`: deploys the Cloudflare relay to your own
  account in one command, and updates it when run again.
- MIT license, `SECURITY.md`, `CONTRIBUTING.md` and this changelog. Release
  archives carry `LICENSE` and `THIRD_PARTY_LICENSES`.
- The Cloudflare relay keeps Workers Logs, so relay errors can be diagnosed
  after the fact.
- CI runs `govulncheck`; Dependabot watches Go modules, npm and Actions.

### Security

- Pairing now proves that each side holds the identity key it presents, so
  someone who has the code cannot pass off another machine's key as theirs.
  **Pairing needs 0.2.2 or later on both machines**; an older machine is
  refused with a message to update. Machines already paired keep working.
- A managed session sends files only from the folder it was offered, never
  from folders added with `allow-path` for your own chats.
- On macOS, `reset-identity` can no longer come back as the old identity when
  the Keychain refuses the new one. `reset-identity` now unpairs every peer
  it can reach first, and names the ones that must unpair this machine.
- A peer can no longer fill your disk through one link: chat and task
  updates are limited to 60 a minute (burst 120) and 1000 unread items or
  32 MiB per link; extra ones are dropped and status says so.
- `control.stale_prekey` replies are bounded (fresh messages only, 10 per
  peer per minute), and an approved task is never delivered twice.
- Relay (Cloudflare): a key that is not a member, or a join on an unknown
  pairing room, stores nothing; each member may hold at most 8 open pairing
  rooms; a malformed `PUBLIC_ORIGIN` is refused instead of failing every
  login. The conformance suite covers these, request signatures bound to
  method, path and body, and malformed timestamps.

### Changed

- The quick start begins with deploying your own relay.
- A relay request that fails inside the relay is answered with an
  `internal` error for that request only, and the connection stays open
  (relay-v1); the relay retries a failed call between mailboxes once, and
  logs the error with Cloudflare's flags.
- Status names dropped messages by cause (corrupt, from unknown machines,
  from paused machines); stale presence pings after being offline are no
  longer counted.
- Release binaries are built with Go 1.26.8, which fixes standard library
  vulnerabilities in `net/http`, `crypto/tls`, `crypto/x509`, `net/url`,
  `html/template`, `encoding/asn1` and `net/textproto`.

### Fixed

- A relay that accepts the connection and then drops it no longer makes the
  daemon reconnect every second: the reconnect delay keeps growing (up to 5
  minutes) until a connection stays up for a minute.
- A machine whose own relay connection is down no longer closes its links
  with `presence_timeout`: only silence after a ping that actually left,
  while connected, counts against a peer.
- A peer that has no relay mailbox yet (just paired, or set up again) no
  longer loses messages: they wait and go out once it has one, and status
  says how many are waiting. A link request that never left before it timed
  out is reported as "not sent: <alias> has no mailbox on the relay yet".
- A relay request that never gets an answer fails after 30 seconds and
  the connection is replaced, instead of holding up every outgoing message;
  `kill` no longer waits behind such a request.
- `cravv-connect daemon stop && cravv-connect daemon start` no longer fails
  now and then with "daemon did not start within 5s": stop waits until
  launchd has unloaded the job and the old process has exited, start loads
  the job again rather than kickstarting one that is going away, and start
  waits up to 15 seconds and shows the end of the daemon's stderr log when
  it gives up. On Linux, start clears a unit's failed state first.
- `cravv-connect daemon start` on a machine that is not set up says to run
  `cravv-connect setup` at once, and a daemon that exits while starting is
  reported right away with its error output.
- Network waits are bounded: a relay dial gives up after 30 seconds (and
  the kill switch stops one in progress), a file chunk request after 2
  minutes (it then counts as a failed attempt), and any HTTP response
  header after 30 seconds.
- When the relay says a peer no longer allows this machine, its links now
  close and its tasks end, as when the peer says it paused you. `peers` and
  the web UI show such a peer as "paused or unpaired you".
- A peer that was offline for more than a week (longer than the relay
  keeps a prekey announcement) is sent this machine's current prekey again
  as soon as it sends anything sealed to an older one.

## v0.2.1 (2026-09-28)

### Fixed

- In-chat approvals work on Claude Code versions that speak MCP protocol
  2026-07-28, which no longer lets a server ask a question in the middle of
  a tool call: `review_pending` returns the forms as input requests instead.
  Before this, those clients fell back to the password prompt.

## v0.2.0 (2026-09-27)

First release of cravv-connect v2: session-to-session links, the chat as the
hub (requests and tasks wake the chat and are accepted or rejected in it),
presence, managed sessions, one-command setup with join codes, the local
web UI, per-link permissions (messages, tasks you approve, automatic tasks),
pause, unpair and the kill switch, and prebuilt releases for macOS and Linux
with checksums and build provenance.
