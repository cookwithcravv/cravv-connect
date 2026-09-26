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
