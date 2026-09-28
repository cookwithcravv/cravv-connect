# Security policy

cravv-connect lets AI agents on different machines message each other and
hand each other tasks, so security problems matter. Thank you for reporting
them.

## Reporting a vulnerability

Report privately through a GitHub security advisory:
https://github.com/cookwithcravv/cravv-connect/security/advisories/new

Please do not open a public issue, pull request or discussion for an
unfixed vulnerability. Include:

- the cravv-connect version (`cravv-connect version`) or commit;
- your platform (macOS or Linux, arm64 or amd64) and the relay you use
  (`relay-cf` or `cravv-relay`);
- what an attacker needs (a paired peer, a relay operator, a local process,
  a message an agent reads) and what they gain;
- steps to reproduce, or a test.

We aim to acknowledge a report within 3 working days and to agree on a fix
and disclosure date with you. We credit reporters in the release notes
unless you ask us not to.

## Supported versions

Only the latest release gets security fixes. cravv-connect is pre-1.0, so a
fix may ship in a release that also changes behaviour; the
[changelog](CHANGELOG.md) says so when it does.

## Scope

In scope: the `cravv-connect` binary (daemon, CLI, MCP server, web UI,
hooks), the relays in `relay-cf/` and `cmd/cravv-relay`, the protocols in
`protocol/`, and `scripts/install.sh`. The threat model, including what is
deliberately out of scope (for example other processes running as your own
user), is in [docs/security.md](docs/security.md). A report that shows the
threat model is wrong is welcome too.

If you run your own relay and suspect its admin token leaked, rotate it:
generate a new token file and run `relay-cf/scripts/deploy.sh` with
`TOKEN_FILE` pointing at it (or `npx wrangler secret put ADMIN_TOKEN`). The
token is only needed to register a relay's first machine.
