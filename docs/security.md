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
  guess per code. Each side signs the exchange with its identity key, so
  even someone who has the code cannot pass off another machine's identity
  as its own (a peer on 0.2.1 or older cannot sign and is refused: update
  both machines). `setup --join` shows the relay in a join code and asks
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
  - It cannot fill your disk through a link: each link may add at most 60
    messages and task updates a minute here (up to 120 at once), and at
    most 1000 items or 32 MiB its session has not read yet. Anything over
    is dropped (and confirmed, so the sender stops resending it), written
    to the audit log at most once a minute per link, and `status` says
    "link N: <alias> is sending too fast, dropped K messages" for an hour.
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
    disconnect, pause, unpair, kill, and closing any session with
    `cravv-connect session close`);
  - read and change `store.db` (on Linux it also holds the identity seed),
    edit the audit log, and read files you received;
  - share a session of their own, which peers could then ask to link with;
  - take over a chat's session while it is away (for example while Claude
    Code restarts), by sharing its name from the same folder as the same
    agent (see "Tokens and session identity");
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
  still marks links that stopped answering away within 150 seconds and
  closes them after the away grace (10 minutes; a managed session's idle
  timeout).
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
what arrives waits; then it closes and its links close. The MCP server
reconnects and reattaches on its own as soon as the daemon answers again.

**Sharing a name again (a trade-off).** A chat that restarted (Claude Code
closed and opened again) has lost its reattach token. So that it gets its
session and links back without asking anyone, `session_share` with the
name of an **away** live session, from a connection registered with the
**same agent and project folder**, takes that session over: same links,
new tokens, and the old tokens stop working. This is a reattach checked by
agent and folder instead of by the token, so any process of your user that
registers as that agent in that folder could take over a session while it
is away. Processes of the same user are already outside what cravv-connect
isolates (above: they can run the CLI and use every no-password action),
so this adds no new reach. It does not apply to an **open** session (the
share fails: "a session named <name> is open in another chat; close it
there or pick another name"), to another agent or folder, or to a managed
session.

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
| Turn the kill switch off | `cravv-connect resume` |
| Allow another folder for outgoing files | `cravv-connect allow-path <dir>` |
| New identity | `cravv-connect reset-identity` |

Does not need it: rejecting a link request (`link reject`), restricting or
disconnecting a link (`link restrict`, `link disconnect`, or `link permit`
to a lower level), `pause`, `resume-peer`, `unpair`, closing a managed
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

In every run mode, files a run sends (`send_file`, files attached to a
task result) must be inside the offer's folder. The folders you allowed
with `cravv-connect allow-path` count for your own chats only, never for a
run.

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
    list for a moment. If the Keychain refuses the write, any older item is
    removed and the seed goes to the fallback below (a seed in the fallback
    wins over a Keychain item); if the older item cannot be removed, the
    write fails, so a reset never comes back as the old identity. If the
    Keychain cannot be read and there is no fallback copy, the daemon
    refuses to start instead of creating a new identity.
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
- `cravv-connect reset-identity` (also while killed) first unpairs every
  peer: it sends each one `control.unpaired` directly (best effort) and
  denies it on the old mailbox. It then turns the kill switch on (or leaves
  it on), deletes every peer, prekey and queued outgoing message, and
  replaces the identity. A peer the notice could not reach (this machine
  was offline or already killed) is named in the output and still lists the
  old machine until its human runs `cravv-connect unpair` there. The new identity has no relay mailbox:
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
