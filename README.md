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
| Disconnect a link | `cravv-connect link disconnect <link>`, the chat's `disconnect`, the web UI's Sessions page | Closes the link on both sides. Its unfinished tasks fail (`link_closed`). Closed links never reopen |
| Restrict a link | `cravv-connect link restrict <link> <level>`, the chat's `restrict`, the web UI's Sessions page | Lowers what the other session may do here, at once; tasks still waiting are rejected when it drops to `messages` |
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
| `daemon start` / `daemon stop` / `daemon status` | Control the daemon. `start` also restarts a running daemon of another version (after an upgrade replaced the binary), and says so: "Restarted the daemon (was v1.2.0, now v2.0.0)"; `setup` does the same. `stop` asks the daemon over its socket to shut down and waits up to 10 seconds for it to exit; it never signals a process that does not answer on the socket |
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
| `link permit <link> <messages\|tasks-ask\|tasks-auto>` | Set what the other side of a link may do here (password to raise it; lowering needs none) |
| `link restrict <link> <messages\|tasks-ask>` | Lower what the other side of a link may do here, at once (no password; never raises) |
| `link disconnect <link>` | Close a link on both sides; its unfinished tasks fail and it never reopens (no password) |
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
| `files accept <file-id>` | Download a file held for a human before the upgrade to v2 (no link holds files now; password) |
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
