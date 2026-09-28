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

### Changed

- The quick start begins with deploying your own relay.
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
