# cravv-connect security

This page says what cravv-connect protects against, what it does not, and the
exact limits behind each claim. Protocol details are in
`protocol/peer-v1.md`, `protocol/relay-v1.md` and `protocol/ipc-v1.md`.

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
  only after its signature verifies. The relay never sees message contents,
  file contents, file names, aliases, or session names.
- **Network attackers.** Everything above, plus TLS to the relay in
  production (`https`/`wss`). `cravv-connect init` accepts an `http://` relay
  URL for local testing; relay addresses learned from peers must be `https`
  except on `localhost`. Mailbox logins and blob requests are signed over the
  relay's normalized origin, so a signature made for one relay cannot be
  replayed against another, and blob request signatures expire after 5
  minutes.
- **Strangers.** A relay mailbox only accepts frames from identity keys its
  owner allowed, which happens only when pairing (and again when you resume a
  peer you paused). Creating a mailbox needs the relay admin token (first
  machine) or a single-use invite that a member requests and sends inside the
  encrypted pairing exchange. Joining a pairing needs the full bind code: the
  relay sees only the 4-character nameplate, the 40-bit secret is the SPAKE2
  password, a room allows exactly one joiner and lives 10 minutes, and a wrong
  code fails key confirmation and burns the room. An attacker gets one online
  guess per code. Relays also cap abuse: at most 20 unused invites per member,
  2 GiB of live blobs per member and 50 GiB per relay, a 10000-frame or 50 MB
  queue per mailbox, and per-mailbox and per-IP rate limits (the Go reference
  relay also allows at most 8 open pairing rooms per member; relay-cf allows
  at most 256 live blobs per member).
- **A paired peer that turns hostile, within its trust level.** The receiving
  daemon enforces the trust level in a policy gate in front of the task and
  file handlers; the agent is never trusted to.
  - A chat-only peer's tasks are always rejected, and its files wait for a
    human to accept them with the password.
  - An ask-first peer's tasks wait for a human to approve them with the
    password; they expire after 24 hours. Agents cannot read a held task's
    text: `get_task` does not return tasks awaiting approval or rejected.
  - Approving a task or accepting a held file checks the peer again: it is
    refused when the peer was unpaired, paused, or lowered in the meantime.
    Pausing or unpairing a peer rejects its tasks awaiting approval and
    declines its held files.
  - Queued tasks expire if no agent claims them within 24 hours.
  - A peer cannot raise its own trust, because trust is a local setting that
    only the human on this machine can raise, with the password.
  - A peer cannot choose where files are saved: every incoming file goes to
    `files/<alias>/<message id>-<sanitized name>` inside the state directory,
    is never written over an existing file, and never becomes a dotfile.
  - A peer cannot clear or rewrite another peer's outbox, update tasks it did
    not receive, or cancel tasks it did not send.
  - A peer cannot choose IDs that fake local output: every message, task and
    file ID it sends must be exactly 26 upper-case Crockford base32
    characters (what `core.NewID` makes), and every blob ID 1 to 64
    characters of `[a-z0-9]`. A message with any other ID is dropped on
    receipt and not retried, so control characters, escape sequences or
    look-alike lines never reach approval screens, logs or agent prompts.
- **Prompt injection that tries to make a local agent pair, raise trust, or
  approve tasks.** All three need the login password (next section), which
  agents do not have.

## What we do not defend against

- **Prompt injection through free text.** A message can still try to talk an
  agent into doing something within that agent's own permissions. Mitigations:
  - items returned by `check_inbox` and `wait_for_message` are wrapped in
    `<remote_message ...>`. The body is XML-escaped, and invisible characters
    (Unicode tag characters, bidi overrides and isolates, zero-width
    characters, the BOM) are removed. Attribute values are cleaned and capped
    at 64 characters. The MCP server's standing instructions tell the agent
    that this content is not from the user and that chat is information, not
    a command;
  - the task tools (`get_task`, `claim_task`, `update_task`,
    `complete_task`, `fail_task`, `cancel_task`) and the JSON agent commands
    return text the peer wrote (an inbound task's instructions and file
    names; an outbound task's result, the peer's progress notes and result
    file names) only in the task's `wrapped` field, with the same wrapper and
    escaping. The raw fields are left empty, so no tool hands an agent
    unwrapped peer text. The remaining plain fields (state, IDs, local alias,
    notes written on this machine) are not peer text, except the peer's
    session name (`claimed_by` on an outbound task, `session` on inbox
    items), which is cleaned like a wrapper attribute: no control, bidi or
    invisible characters, at most 64 characters;
  - hooks never print message bodies or names chosen by a peer, only local
    aliases and counts;
  - the CLI removes every control character (including newlines, carriage
    returns, tabs and escape sequences), bidi and zero-width characters from
    peer-derived strings it prints (IDs, aliases, file names, error
    messages), and prints multi-line task text in `cravv-connect approvals`
    with each line prefixed by `| `, so it cannot pass for the CLI's own
    lines;
  - outgoing files are restricted (below);
  - your agent's own permission settings remain the last line of defense.
    Keep them as strict as you would for untrusted input.
- **Other processes running as the same OS user.** They can read the identity
  key (Keychain item or `store.db`), read and change the SQLite store, talk to
  the daemon socket, and edit the audit log. The audit log is not
  tamper-evident.
- **Anyone who knows the login password**, or a setup with passwordless sudo
  that an agent can use to read the password or change the daemon.
- **Traffic metadata visible to the relay** (section "What the relay sees").
- **A relay that drops control messages.** Control messages (such as a pause
  or prekey notice) leave the sender's outbox once the relay has queued them
  and are not resent, so a relay that drops one is not detected.
- **Secrets pasted into messages.** Free text cannot be policed. An agent can
  still put a secret into a chat message or a task result.

## The password gate

**Only type your login password into the `cravv-connect` CLI in your own
terminal.** No agent, web page, or message will ever legitimately ask you for
it.

How it works:

- The CLI reads the password from `/dev/tty` with echo off, never from stdin
  or an argument, so a process without a terminal (such as an agent running a
  shell command) cannot be prompted.
- The CLI sends it over the local socket (mode `0600`, directory `0700`) in
  `auth.unlock`. The daemon checks it with PAM for the user the daemon runs
  as. Only services that check the login password are allowed: `chkpasswd`
  (default) or `checkpw` on macOS, `login` (default) or `system-auth` on
  Linux, set with `pam_service` in `config.toml`; any other value is refused.
  After the password, PAM's account check must also pass (expired or disabled
  accounts are refused).
- The first time the daemon starts with a PAM service, and again whenever
  that service's file in `/etc/pam.d` changes (size or modification time), it
  checks that a random password is rejected and refuses to start otherwise.
  This counts as one failed login for that account.
- PAM needs cgo; a binary built with `CGO_ENABLED=0` refuses every
  password-gated action (the CLI reports "password check unavailable").
- A successful check unlocks only that one connection, for 10 minutes. It is
  not stored anywhere.
- The daemon checks the connection's unlock again inside each human-only
  action (approving or denying, accepting a file, raising trust, `resume`,
  `allow-path`, `reset-identity`, starting, joining and finalizing a
  pairing), so a missing check in one layer does not open the gate.
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
| Pair and join | `cravv-connect pair`, `cravv-connect join <code>` |
| Raise a peer's trust | `cravv-connect trust <alias> <level>` (raising only) |
| List, approve or deny held tasks | `cravv-connect approvals`, `approve <id>`, `deny <id>` |
| Accept a held file | `cravv-connect files accept <id>` |
| Turn the kill switch off | `cravv-connect resume` |
| Allow another folder for outgoing files | `cravv-connect allow-path <dir>` |
| New identity | `cravv-connect reset-identity` |

When a task is held, hooks tell the agent to have you run
`cravv-connect approvals`, and on macOS a desktop notification names the local
alias. Even listing held tasks needs the password, so agents never read their
text.

Does not need it (agents and humans can both do these):

- sending, reading, and claiming, updating, completing, failing or cancelling
  tasks;
- `pause`, `resume-peer`, `unpair`, lowering trust, `kill`, stopping the
  daemon (`cravv-connect daemon stop`, IPC `daemon.shutdown`).

The rule is: anything that widens what a peer or an agent can do needs the
password; anything that narrows it does not. `resume-peer` is the one
exception without a password, because it only restores a state the human
already chose when pairing.

## What agents can and cannot do

Through MCP or the JSON agent commands (`cravv-connect send`, `inbox`,
`wait`, `task ...`), an agent can:

- send chat, tasks and files to paired machines, read its inbox, and wait for
  messages;
- claim and work on tasks that the receiving daemon queued (from autonomous
  peers, or approved by a human), and cancel tasks it sent;
- pause, unpair, lower trust, pull the kill switch (again, if it is already
  on), and stop the daemon;
- through the CLI, also resume a peer, rename aliases, list files, and read
  the audit log.

A task claimed through the JSON CLI fails as `abandoned` 7 days after the
claim if it is not finished; a task claimed through MCP fails as `abandoned`
when its session has been gone for 5 minutes.

An agent cannot:

- pair or join, raise trust, list or decide held tasks, accept held files,
  allow new folders, turn the kill switch off, or reset the identity;
- send files from outside the folder its session registered with or the
  folders the human allowed. The session's folder is the agent process's
  working directory, which the agent chooses, so it is not a boundary the
  human sets: keep agents that must not read your files away from this tool;
- send any hidden file or folder (any path part starting with a dot), or
  files whose names look like secrets: `.env*`, `id_*`, `credentials*.json`,
  `service-account*.json`, and `*.pem`, `*.key`, `*.env`, `*.p12`, `*.pfx`,
  `*.jks`, `*.keystore`, `*.kdbx`, `*.ppk`, `*.ovpn` (case-insensitive). Only
  regular files up to 100 MB with a single hard link are sent. Symlinks are
  resolved and checked again, and the file is opened without following links
  and must be the file that was checked. Every uploaded file is written to
  the audit log with the peer, path, size and SHA-256;
- see a peer-chosen alias in hooks, notifications, approval lists or CLI
  tables: those use the local alias.

`cravv-connect kill` (or the agent's `kill_switch`) is saved first and
survives restarts. Tasks an agent claimed here are failed as `killed`, and
their senders are told if the daemon can reach the relay within 3 seconds.
The switch counts as on from the start of that window: agent calls are
refused and incoming frames are left unhandled while the daemon flushes only
those failure updates. The daemon then disconnects from the relay, stops downloads, stops uploads
in progress (the partial blob is deleted and no offer is sent), and stops
handling incoming frames, which wait at the relay for up to 7 days. Messages
the relay already accepted are still delivered to peers; nothing new is sent
until `cravv-connect resume`. Every IPC method except `status`,
`auth.unlock`, `resume`, `kill`, `daemon.shutdown`, `reset_identity`,
`peer.list`, `audit.read` and `hook.counts` fails, so agents cannot send,
read the inbox or work on tasks, and `pause`, `unpair` and trust changes also
wait until resume. `reset-identity` still works (with the password), so a
machine killed because it may be compromised gets a new identity without
reconnecting under the old one first. Pulling the switch again while it is on succeeds and
changes nothing.

## Keys and local storage

- **State directory:** `~/.cravv-connect` (or `CRAVV_HOME`), mode `0700`.
  `config.toml`, `store.db`, `audit.log`, `daemon.sock`, `daemon.pid` and
  the daemon logs are `0600`;
  `files/` and its folders are `0700` and received files `0600`.
- **Identity key (Ed25519 seed):**
  - macOS: a generic password (service `cravv-connect`, account `identity`,
    base64 seed) in the default keychain, normally the login keychain,
    written through `security(1)`. The seed is passed to `security` as an
    argument, so another process of the same user could see it in the process
    list for a moment. If the Keychain refuses the write, the seed goes to the
    fallback below; if the Keychain cannot be read and there is no fallback
    copy, the daemon refuses to start instead of creating a new identity.
  - Linux (and the macOS fallback): the `settings` table of `store.db`,
    base64 encoded, protected only by file permissions.
- **Prekeys:** private X25519 keys in `store.db`. The current one is rotated
  every 7 days (not while the kill switch is on), and superseded ones are
  deleted 21 days after they were replaced, which bounds how much past
  traffic a stolen `store.db` can open.
- **Messages and files** are stored in plaintext locally: the inbox in
  `store.db` (30 days), sent messages in the outbox (up to 21 days), tasks
  with their text and results (kept until you delete the store), and received
  files under `files/<alias>/` (never deleted automatically). The relay admin
  token given to `init --relay-token` is kept in `store.db` until the first
  successful registration.
- `cravv-connect reset-identity` (also while killed) turns the kill
  switch on (or leaves it on), deletes every peer, prekey and queued outgoing message, and
  replaces the identity. The new identity has no relay mailbox: it registers
  again with an admin token or with the invite received the next time you
  join a pairing. Every peer has to pair again; run `cravv-connect resume`
  when ready.

## Audit log

`~/.cravv-connect/audit.log` is append-only JSON lines (`cravv-connect log`
shows it). It records pairing, unpairing, trust changes, pause and resume,
kill and resume, approvals and denials, every password attempt, allowed
folders, identity resets, every incoming task, every accepted file, every
completed download and every outgoing file, with a timestamp and, where they
apply, the local alias and machine ID, the item ID and a content hash. Agents
can read it with `cravv-connect log`.

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
  IDs (which contain a millisecond timestamp);
- the prekey ID in each frame header, and so when each machine rotates
  prekeys;
- pairing nameplates, which member created each room, the joiner's IP address
  and when rooms are used (never the secret part of the code);
- blob sizes, chunk counts, uploader and recipient keys, and download times.

It cannot see message contents, task text, file contents, file names, aliases,
device names, or session names: all of those are inside encrypted frames or
the encrypted pairing payload.

## Reporting a vulnerability

Please report security problems privately: open a private security advisory
on the project's repository, or contact its maintainers directly. Do not open
a public issue for an unfixed vulnerability. Include the version or commit,
your platform, and steps to reproduce. For a relay deployment you operate,
also rotate the relay admin token (`wrangler secret put ADMIN_TOKEN` for the
Cloudflare relay, or restart `cravv-relay` with a new
`CRAVV_RELAY_ADMIN_TOKEN`) if you suspect it leaked.
