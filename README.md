# cravv-connect

cravv-connect lets AI coding agents on different machines talk to each other.
A Claude Code session on your laptop and a Codex session on a GPU box can send
each other messages, hand each other tasks and get the results back, and send
files, all end-to-end encrypted through a relay you deploy.

- Works with any agent that can run an MCP server (Claude Code, Codex, Cursor,
  VS Code Copilot, Gemini CLI) or a shell command.
- Machines pair with a one-time bind code such as `CRAVV-7K3F-9QXM-TR2A`.
- Each machine decides how much it trusts each peer: chat only, ask first, or
  autonomous.
- Pairing, raising trust, and approving tasks need your login password, so no
  agent (local or remote) can do them on its own.
- Any connection can be paused, removed, or cut off with a kill switch.
- The relay only ever sees ciphertext and routing metadata.

Read [docs/security.md](docs/security.md) before you rely on it. In short:
cravv-connect stops a peer or an injected prompt from pairing, raising trust,
or approving tasks, but a message can still try to talk your agent into
something within its own permissions. Keep your agent's permission settings
strict.

**The only place to type your login password is the `cravv-connect` CLI in
your own terminal.**

## How it works

```
 Machine A                                   Machine B
 agent -- MCP/CLI --> cravv-connect daemon   cravv-connect daemon <-- MCP/CLI -- agent
                              |                        |
                              +------> relay <---------+
                          (WebSocket + HTTPS, only ciphertext)
```

One Go binary, `cravv-connect`, is the daemon (the only part that uses the
network), the MCP server (`cravv-connect mcp`), the hook
(`cravv-connect hook`), and the CLI. The relay is either the Cloudflare
Worker in [`relay-cf/`](relay-cf/README.md) for real use, or the Go
reference relay `cravv-relay` for local testing. Protocols:
[relay-v1](protocol/relay-v1.md), [peer-v1](protocol/peer-v1.md),
[ipc-v1](protocol/ipc-v1.md).

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
   everything in memory and stops when the machine restarts.
2. **Admin token**, only if this is the relay's first machine (setup makes
   one for a test relay it starts).
3. **This machine and the daemon.** It writes `~/.cravv-connect/config.toml`,
   installs the daemon as a login service (launchd on macOS, systemd on
   Linux) and waits until it is connected to the relay.
4. **Agents.** It detects Claude Code and Codex and offers to add
   cravv-connect to each.
5. **Pair a device.** It shows a join code and its QR code, and waits for
   the other machine.

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
for a local name for the other machine, and offers the agents.

A join code works once and expires in 10 minutes. To pair two machines that
are already set up, run `cravv-connect pair` on one and
`cravv-connect join <code>` on the other (the join code or the plain bind
code).

### 3. Use it from Claude Code

Restart Claude Code and type `/cravv` in a chat. The chat is shared as a
session, and sessions on paired machines can ask to link with it; you decide
each link.

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

### Manual setup: the first machine

```sh
cravv-connect init --relay https://cravv-relay.example.workers.dev --relay-token <admin token>
cravv-connect daemon install      # launchd (macOS) or systemd --user (Linux); starts it now
cravv-connect status
```

The admin token is used once, to create this machine's mailbox, and then
deleted. Use `--relay-token -` to read it from stdin, and `--name` to choose
the device name peers see as a suggestion (default: the host name up to the
first `.`; lowercased to letters, digits and dashes, at most 24 characters).

### Manual setup: the other machine

```sh
cravv-connect init --relay https://cravv-relay.example.workers.dev
cravv-connect daemon install
```

No token: this machine gets its mailbox from an invite that arrives during
pairing.

`daemon install` points the service at the running binary with symlinks
resolved, so install a built binary (for example `make build`, then
`bin/cravv-connect daemon install`), not `go run`: it refuses paths under the
temporary directory or Go's build cache. It then waits up to 10 seconds for
the daemon to answer and, if it does not, says where the logs are.

### Manual setup: pair

On the first machine:

```sh
cravv-connect pair
# Login password for this machine:
# Join code: cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A
# (a QR code of it, and the plain bind code CRAVV-7K3F-9QXM-TR2A)
```

Share the code any way you like (it works once and expires in 10 minutes). On
the other machine:

```sh
cravv-connect join CRAVV-7K3F-9QXM-TR2A
```

Both sides ask for the login password, then for a local name for the peer.
`cravv-connect peers` lists paired machines with their machine IDs, so you can
compare them later.

### Manual setup: connect your agents

```sh
cravv-connect install claude      # MCP server plus UserPromptSubmit and Stop hooks
cravv-connect install codex       # adds [mcp_servers.cravv-connect] to ~/.codex/config.toml
```

Restart the agent afterwards. Cursor, VS Code, Gemini CLI, and agents without
MCP: see [docs/agents.md](docs/agents.md).

## Using it from an agent

Ask your agent in plain words, for example "send gpu-box the failing test
output" or "ask gpu-box to run the eval on checkpoint 12 and wait for the
result". The MCP tools are:

| Tool | Purpose |
|---|---|
| `status` | This machine, this session, peers, pending counts |
| `send_message(to, text)` | Chat, up to 64 KB |
| `check_inbox(limit?)` | Unread items for this session, wrapped as untrusted content (pages of at most 4 MiB; call again for the rest) |
| `wait_for_message(timeout_s?)` | Wait up to 50 seconds (the default) for a new item or task update |
| `create_task(to, instructions, file_paths?)` | Send a task; returns `task_id` |
| `get_task`, `claim_task`, `update_task`, `complete_task`, `fail_task`, `cancel_task` | Work on tasks; text the other machine wrote (instructions, results, its notes, file names) comes only in the `wrapped` field |
| `send_file(to, path)` | Send a file from the project folder (up to 100 MB) |
| `pause_peer`, `unpair_peer`, `lower_trust`, `kill_switch` | Cut-off controls |

`to` is a peer alias (`gpu-box`, every session there) or an alias and a
session (`gpu-box/codex@training`); `send_file` takes a peer alias only. Sessions are named `<agent>@<folder>`.
Everything that arrives is wrapped like this (inbox items, and the peer's
part of a task in `wrapped`), and agents are told never to treat it as your
instructions:

```
<remote_message from="gpu-box" session="codex@training" trust="autonomous" id="01J..." kind="task" task_id="01J...">
...escaped body...
</remote_message>
```

The daemon only accepts message, task and file IDs in its own format (26
upper-case Crockford base32 characters), and the CLI strips control, bidi
and invisible characters from anything a peer chose before printing it, so
a peer cannot fake lines in your terminal.

In Claude Code, the hook adds a one-line notice such as
`cravv-connect: 2 new messages from gpu-box. Use check_inbox.` to your prompt.

## Trust levels

Each machine sets the level for traffic **from** each peer; the two
directions are independent.

| From a peer at | Chat | Tasks | Files |
|---|---|---|---|
| `chat-only` | Delivered | Rejected automatically | Held until you run `cravv-connect files accept <id>` (password) |
| `ask-first` (default) | Delivered | Held until you approve with `cravv-connect approvals` (password); expire after 24 hours. On macOS you also get a desktop notification | Downloaded, within the 1 GB per-peer quota (counting the last 30 days) |
| `autonomous` | Delivered | Queued; an agent session can claim it at once (expires after 24 hours if nobody claims it) | Downloaded, within the quota |

`cravv-connect trust <alias> <level>` changes it. Raising asks for your
password, lowering does not. Lowering to `chat-only` rejects that peer's
pending tasks.

Files you send are limited to the session's project folder and folders you add
with `cravv-connect allow-path <dir>` (password). Hidden files and folders,
and secret-looking files such as `.env*`, `id_*`, `*.pem`, `*.key`, `*.p12`,
`*.pfx`, `*.jks`, `*.keystore`, `*.kdbx`, `*.ppk`, `*.ovpn`,
`credentials*.json` and `service-account*.json` (any case), are always
refused.

## Cut-off controls

Agents and humans can use all of these without a password. While the kill
switch is on, only `resume` (and `status`, `peers`, `log`, `kill` again, and
`daemon stop`) works:

| Control | Command | Effect |
|---|---|---|
| Pause | `cravv-connect pause <alias>` | Stops traffic with that peer in both directions. Your sends fail with "paused"; theirs are held on their machine. Its tasks waiting for approval are rejected and its held files declined. Undo with `resume-peer <alias>`. |
| Unpair | `cravv-connect unpair <alias>` | Removes the peer and its keys on both sides (best effort for the notice). Pairing again needs a new code. |
| Lower trust | `cravv-connect trust <alias> chat-only` | Takes effect immediately. |
| Kill switch | `cravv-connect kill` | Fails claimed tasks (and tells their senders when it can), stops file downloads, disconnects from the relay, and stops handling incoming messages (they wait on the relay). Until you run `cravv-connect resume` (password), every command and agent operation is refused except `status`, `peers`, `log`, `kill` (a no-op) and `daemon stop`. Messages already in the outbox go out after resume. Survives restarts. |

## CLI reference

| Command | What it does |
|---|---|
| `setup [--relay <url>] [--relay-token <t>] [--name <n>] [--yes] [--no-agents] [--pair] [--reset]` | Guided setup: relay (or a LAN test relay), init, the daemon, agents, pairing. Safe to run again |
| `setup --join <join code> [--name <n>] [--yes] [--no-agents] [--reset]` | Set this machine up on the relay in a join code and pair with the machine that showed it |
| `version` | Print the version |
| `init --relay <url> [--relay-token <t>] [--name <n>] [--force]` | Write `config.toml`; store the admin token for the first machine. The relay URL must be an origin, `scheme://host[:port]`, with no path. `--force` with a different relay clears this machine's relay registration so it registers again there; peers are not told, so re-pair with them |
| `daemon run [--log-file <path>]` | Run the daemon in the foreground. Logs JSON to stderr, or with `--log-file` to that file, rotated at 10 MiB with 3 old files kept |
| `daemon start` / `daemon stop` / `daemon status` | Control the daemon. `stop` asks the daemon over its socket to shut down and waits up to 10 seconds for it to exit; it never signals a process that does not answer on the socket |
| `daemon install` / `daemon uninstall` | Run the daemon at login (launchd or systemd user unit) |
| `status [--json]` | Relay connection, peers, queues, pending approvals, sessions, errors |
| `pair [--no-qr]` | Show a join code (with a QR code) and pair (password) |
| `join <code>` | Join with a join code for this machine's relay, or a bind code (password) |
| `peers` | List peers with trust, state and machine ID |
| `alias <alias> <new-alias>` | Rename a peer locally |
| `trust <alias> <chat-only\|ask-first\|autonomous>` | Set a peer's trust (raising needs the password) |
| `pause <alias>` / `resume-peer <alias>` | Pause or resume a peer |
| `unpair <alias> [-y]` | Remove a peer |
| `approvals` | Review pending tasks interactively (password once; again after 10 minutes) |
| `approve <task-id>` / `deny <task-id>` | Decide one pending task (password) |
| `files` | List incoming and outgoing files |
| `files accept <file-id>` | Download a held file (password) |
| `allow-path <dir>` | Allow sending files from another folder (password) |
| `kill` / `resume` | Kill switch on; off (password) |
| `reset-identity` | New machine identity; every peer must pair again (password) |
| `log [-n N]` | Recent audit log entries |
| `install [claude\|codex]` / `uninstall <agent>` | Add or remove the MCP server and hooks for an agent; no argument lists agents |
| `mcp [--project-dir <dir>]` | The stdio MCP server (started by your agent) |
| `hook` | One-line unread notice for agent hooks |
| `send <to> <text\|->` | Agent CLI: send chat (JSON output) |
| `inbox [--limit N]` | Agent CLI: read unread items (JSON) |
| `wait [--timeout S]` | Agent CLI: wait up to 50 seconds (JSON) |
| `task create <to> <instructions\|-> [--file <path>]...` | Agent CLI: send a task (JSON) |
| `task get\|claim\|cancel <task-id>` | Agent CLI: task operations (JSON); a task claimed this way fails as abandoned if not finished within 7 days |
| `task update <task-id> <note\|->` | Agent CLI: progress note (JSON) |
| `task complete <task-id> <result\|-> [--file <path>]...` | Agent CLI: finish a task (JSON) |
| `task fail <task-id> <reason>` | Agent CLI: fail a task (JSON) |

State lives in `~/.cravv-connect` (override with `CRAVV_HOME`): `config.toml`,
`store.db`, `audit.log`, `daemon.log` (rotated: `daemon.log.1` to `.3`),
`daemon-stderr.log` (crash output only), `daemon.sock`, `daemon.pid` (written
once the daemon owns the socket), and received files in `files/<alias>/`. The identity key is in the macOS
Keychain, or in `store.db` on Linux.

`config.toml` keys: `relay_url`, `device_name`, `peer_quota` (bytes, default
1 GiB), and `pam_service` (macOS `chkpasswd`, the default, or `checkpw`;
Linux `login`, the default, or `system-auth`; any other value is refused).

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
| A new machine never connects | It has no mailbox yet. Either `init --relay-token` (first machine only) or pair with `join`, which registers it with an invite. |
| `status` shows `password check is not working: ...` | PAM cannot check passwords (see "Linux password check" above; on a build without cgo, rebuild with `make build`). Password-gated actions fail until it is fixed; restart the daemon afterwards. |
| `password check unavailable: ... built without PAM support` | Rebuild with cgo and the PAM headers (`make build`) and restart the daemon. |
| `pam service not allowed` | Set `pam_service` in `config.toml` to an allowed value (see above), or remove it. |
| `refusing to start: ... password verifier accepted a random password` | The daemon checks once per PAM service (and again after its `/etc/pam.d` file changes) that a random password is rejected. Your PAM service accepts anything; use one that checks your login password. |
| `keychain locked or waiting for a dialog` (macOS) | The daemon could not read its identity from the login Keychain within 10 seconds. Unlock it (`security unlock-keychain ~/Library/Keychains/login.keychain-db`) or start the daemon from your logged-in session, then start it again. |
| `too many failed password attempts; locked` | Five wrong passwords in a row lock it. Wait 15 minutes (restarting the daemon does not reset it). Every attempt is in `cravv-connect log`. |
| `pairing failed: wrong code or the exchange was interrupted` | The code is burned. Run `cravv-connect pair` again for a new one. |
| Sends to a peer say "paused" | You paused it: `cravv-connect resume-peer <alias>`. If `peers` shows "paused by peer", the other side paused you; your messages are held until they resume. |
| `status` warns about timestamps in the future | Fix the clock on one of the machines (messages more than 10 minutes in the future are rejected). |
| A task never runs | Check the receiver's trust level. `ask-first` tasks wait in `cravv-connect approvals`; `chat-only` tasks are rejected. |
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
directories and drives them through the IPC API: pairing, chat, tasks at each
trust level, files, pause, unpair, kill switch, relay restarts, duplicate
delivery, the stale prekey round trip, and an MCP smoke test. Tests use a
fake password verifier and keep the identity in the store instead of the
Keychain.

### Manual two-machine smoke test

If `cravv-connect daemon install` was run on a machine, `daemon start` and
`daemon stop` control the installed service, not a temporary `CRAVV_HOME`:
use `cravv-connect daemon run` in a second terminal there instead.

On machine A (with a relay at `$RELAY` and its admin token `$TOKEN`):

```sh
export CRAVV_HOME=$(mktemp -d)
cravv-connect init --relay "$RELAY" --relay-token "$TOKEN" --name smoke-a
cravv-connect daemon start
cravv-connect pair                      # note the code; alias "smoke-b", trust autonomous
```

On machine B:

```sh
export CRAVV_HOME=$(mktemp -d)
cravv-connect init --relay "$RELAY" --name smoke-b
cravv-connect daemon start
cravv-connect join CRAVV-XXXX-XXXX-XXXX # alias "smoke-a", trust ask-first
cd ~/some-project
cravv-connect wait --timeout 50         # returns within 50 s; run it again if nothing arrived
```

Back on A:

```sh
cd ~/some-project
cravv-connect send smoke-b "hello from A"            # B's wait prints it
echo "print('hi')" > hello.py
cravv-connect task create smoke-b "run hello.py" --file hello.py
cravv-connect status                                 # outbox drains to 0 pending
```

On B: `cravv-connect approvals` (password), approve the task, then
`cravv-connect inbox` shows it; `cravv-connect task claim <id>` and
`cravv-connect task complete <id> "done"`. On A, `cravv-connect wait` shows
the result. Finish with `cravv-connect pause smoke-a` on B (A's sends are
held), `cravv-connect resume-peer smoke-a`, `cravv-connect kill` and
`cravv-connect resume` (password) on either side, and
`cravv-connect unpair <alias>` (type yes, or pass `-y`) on one side (the other
side's `peers` list empties). Stop both daemons with
`cravv-connect daemon stop` and delete the two `CRAVV_HOME` directories.

## License

Not yet licensed for redistribution. Open sourcing is planned.
