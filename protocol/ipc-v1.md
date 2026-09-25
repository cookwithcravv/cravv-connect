# ipc-v1: the local daemon API

Status: normative. Version 1.

ipc-v1 is how local clients talk to `cravv-connect daemon`: the MCP server
(`cravv-connect mcp`), the hook (`cravv-connect hook`), and every CLI command.
Only the daemon touches the network.

Go reference: `internal/ipc` (transport, gates, errors, method names, views)
and `internal/api` (one file per method group).

## 1. Transport

- **Socket:** a unix stream socket at `$CRAVV_HOME/daemon.sock`
  (default `~/.cravv-connect/daemon.sock`). The directory is mode `0700` and the
  socket `0600`, so only the same OS user can connect.
- **Startup:** the daemon removes a stale socket left by a crash, refuses to
  replace anything that is not a socket, and refuses to start when another
  daemon answers on it.
- **Framing:** newline-delimited JSON. Each line is one JSON-RPC 2.0 request or
  response, at most 8 MiB. A longer line closes the connection.
- **Concurrency:** a client may send several requests without waiting; each
  is handled concurrently and answered with its `id`. Requests without `id`
  are notifications and get no response.
- **Connection state:** a session (section 3) and a password unlock (section
  4) belong to one connection. Closing the connection disconnects the
  session; it can be reclaimed within its grace period (section 3).

A client that cannot connect reports:

```text
daemon not running: run `cravv-connect daemon start`
```

## 2. Messages

Request:

```json
{"jsonrpc":"2.0","id":7,"method":"chat.send","params":{"to":"gpu-box","text":"hello"}}
```

Success:

```json
{"jsonrpc":"2.0","id":7,"result":{"id":"01J8ZQ9K7B2V4N6M8P0R2T4W6Y"}}
```

Error:

```json
{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"peer is paused","data":{"kind":"paused"}}}
```

- Missing, empty, or `null` params decode as the zero value. Methods that take
  nothing accept `{}`.
- A method that returns nothing returns `{}`.
- Error codes: `-32700` for a line that is not valid JSON or has no method,
  `-32601` for an unknown method, `-32602` for invalid params (kind
  `bad_request`), `-32000` for everything else. `data.kind` is always set.

## 3. Sessions

`session.register` turns a connection into an agent session:

- The name is `<agent>@<basename of project_dir>`, with `-2`, `-3`, ... added
  when the name is taken, for example `claude@glow-v2` and `claude@glow-v2-2`.
  Each part is lowercased, limited to `a-z 0-9 . _ -` and at most 32
  characters. `pid` is informational and currently ignored.
- A connection may register once.
- If a disconnected session with the same agent and project folder exists and
  was seen within 5 minutes, the new connection gets it back: same name, read
  position and claimed tasks. For the agent `cli` (the `--json` CLI) the grace
  is 30 days. Its claimed tasks are not abandoned when the session expires;
  instead a task claimed by a `cli` session fails as `abandoned` once it has
  been claimed or running for 7 days.
- A new session starts after the newest item that is older than 24 hours and
  that some session already read, so it sees the last day plus unread items
  that arrived after that point.
- When a session ends and is not reclaimed within the grace period, its
  claimed tasks fail as `abandoned` (except for `cli`, see above), and unread
  items addressed to it become machine-wide with the note
  `(originally for <name>)`.

## 4. Gates

Each method has a gate, checked before the handler in this order:

1. **Kill switch.** While the kill switch is on, only `status`, `auth.unlock`,
   `resume`, `peer.list`, `audit.read`, and `hook.counts` run. Everything else
   fails with `killed`.
2. **session:** `session.register` must have succeeded on this connection,
   otherwise `no_session`.
3. **unlock:** `auth.unlock` must have succeeded on this connection within the
   last 10 minutes, otherwise `auth_required`.

The daemon checks the connection's unlock state again for every human-only
action (`approvals.decide`, `files.accept`, `resume`, `allow_path.add`,
`reset_identity`, and `peer.trust` when raising), so a missing unlock fails
with `auth_required` even if a gate were misconfigured.

`auth.unlock` checks the OS login password of the user running the daemon
with PAM: service `chkpasswd` on macOS and `login` on Linux by default, or
`pam_service` in `config.toml`, which must be one of `chkpasswd`, `checkpw`
(macOS) or `login`, `system-auth` (Linux); any other service fails every
unlock with `auth_unavailable`. The first time the daemon starts with a given
PAM service it checks that a random password is rejected, and refuses to
start if it is accepted. After 5 wrong passwords in a row, every attempt fails
with `locked` for 15 minutes without reaching PAM; the count and the lock
survive a daemon restart. Every non-empty attempt is written to the audit log
(never the password); an empty password fails with `bad_password` without
being counted. A binary built without cgo has no PAM and fails every unlock
with `auth_unavailable`.

## 5. Methods

"Kill" is yes when the method still runs while the kill switch is on.

| Method | Params | Result | Gate | Kill |
|---|---|---|---|---|
| `session.register` | `{agent, project_dir, pid}` | `{name}` | none | no |
| `status` | `{}` | `StatusResult` | none | yes |
| `chat.send` | `{to, text}` | `{id}` | session | no |
| `inbox.check` | `{limit}` | `{items: [InboxView]}` | session | no |
| `inbox.wait` | `{timeout_s}` | `{items: [InboxView]}` | session | no |
| `task.create` | `{to, instructions, file_paths?}` | `{task_id}` | session | no |
| `task.get` | `{task_id}` | `TaskView` | session | no |
| `task.claim` | `{task_id}` | `TaskView` | session | no |
| `task.update` | `{task_id, note}` | `TaskView` | session | no |
| `task.complete` | `{task_id, result, file_paths?}` | `TaskView` | session | no |
| `task.fail` | `{task_id, reason}` | `TaskView` | session | no |
| `task.cancel` | `{task_id}` | `TaskView` | session | no |
| `file.send` | `{to, path}` | `{file_id}` | session | no |
| `peer.list` | `{}` | `{peers: [PeerView]}` | none | yes |
| `peer.pause` | `{alias}` | `{}` | none | no |
| `peer.resume` | `{alias}` | `{}` | none | no |
| `peer.unpair` | `{alias}` | `{}` | none | no |
| `peer.alias` | `{alias, new_alias}` | `{}` | none | no |
| `peer.trust` | `{alias, level}` | `{}` | unlock only when raising | no |
| `kill` | `{}` | `{}` | none | no |
| `resume` | `{}` | `{}` | unlock | yes |
| `auth.unlock` | `{password}` | `{expires_at}` | none | yes |
| `pair.start` | `{}` | `{pending_id, code}` | unlock | no |
| `pair.await` | `{pending_id}` | `{pending_id, suggested_name, machine_id}` | unlock | no |
| `join.start` | `{code}` | `{pending_id, suggested_name, machine_id}` | unlock | no |
| `pair.finalize` | `{pending_id, alias, trust}` | `{alias}` | unlock | no |
| `approvals.list` | `{}` | `{tasks: [ApprovalView]}` | unlock | no |
| `approvals.decide` | `{task_id, approve}` | `{}` | unlock | no |
| `files.list` | `{}` | `{files: [FileView]}` | none | no |
| `files.accept` | `{file_id}` | `{}` | unlock | no |
| `allow_path.add` | `{path}` | `{}` | unlock | no |
| `reset_identity` | `{}` | `{}` | unlock | no |
| `audit.read` | `{limit}` | `{events: [Event]}` | none | yes |
| `hook.counts` | `{cwd}` | `{notice, unread, approvals}` | none | yes |

### 5.1 Method notes

- **Addresses.** `to` is a local alias (`gpu-box`), an alias and a session
  (`gpu-box/codex@training`), or a full 52-character machine ID. `file.send`
  ignores a `/session` part.
- **Sizes.** `text`, `instructions`, `note`, `result` and `reason` are at most
  65536 bytes (`too_large`). `project_dir` and `allow_path.add`'s `path` must be
  absolute. Required strings that are empty or only whitespace fail with
  `bad_request`; `result` and `reason` may be empty.
- **`session.register`:** `agent` defaults to `agent` when empty.
- **`chat.send`:** fails with `paused` when this machine paused the peer. When
  the peer paused this machine the message is accepted and held until the
  peer resumes.
- **`inbox.check`:** returns unread items for this session (default 50, at
  most 200) and advances its read position. Items addressed to another
  session are never returned.
- **`inbox.wait`:** like `inbox.check`, but blocks until at least one item
  arrives or `timeout_s` passes (0 or less, or more than 50, means 50). It
  returns `{"items": []}` on timeout. Updates on tasks this session sent
  arrive here too.
- **`task.create`:** `file_paths` are sent as files first (outbound rules as
  for `file.send`); fails with `paused` for a peer this machine paused.
- **`task.get`:** an inbound task that is not awaiting approval or rejected
  (those return `not_found`, so agents never read instructions no human
  approved), or an outbound task this session created (`not_found` for other
  sessions).
- **`task.claim`:** atomic. Fails with `already_claimed` when another session
  holds it and `bad_transition` when the task is not `queued` (for example
  still awaiting approval). Claiming your own claimed task again returns it.
- **`task.update`:** only the claiming session; moves `claimed` to `running`.
- **`task.complete`, `task.fail`:** only the claiming session
  (`not_permitted` otherwise), only while `claimed` or `running`
  (`bad_transition` otherwise). `task.complete` marks the task done before
  sending `file_paths`; a file that cannot be sent is returned as an error and
  the task stays done.
- **`task.cancel`:** only the session that created an outbound task
  (`not_found` otherwise), while it is still active.
- **`file.send`:** `path` is absolute or relative to the session's project
  folder. Refused with `path_refused`: paths outside the project folder and
  allowed folders, any component starting with `.`, names matching `.env*`,
  `id_*`, `credentials*.json`, `service-account*.json` or ending in `.pem`,
  `.key`, `.env`, `.p12`, `.pfx`, `.jks`, `.keystore`, `.kdbx`, `.ppk`, `.ovpn`
  (case-insensitive), symlinks that resolve outside those folders,
  non-regular files, files with more than one hard link, and files over
  100 MiB. Fails with `paused` for a peer this machine paused.
- **`peer.trust`:** `level` is `chat-only`, `ask-first` or `autonomous`.
  Lowering needs no password and takes effect at once (lowering to chat-only
  rejects the peer's tasks that are awaiting approval or queued, and tells the
  sender). Raising needs an unlock.
- **`peer.pause`:** stops traffic both ways: sends `control.paused`, holds the
  outbox, denies the peer on the relay, rejects the peer's tasks awaiting
  approval and declines its held files. Pausing a paused peer does nothing.
- **`peer.resume`:** releases held messages unless the peer has paused this
  machine.
- **`peer.unpair`:** the same cleanup as pause, then deletes the peer and its
  outbox; pairing again needs a new code.
- **`peer.alias`:** `new_alias` is 1 to 24 characters of `a-z 0-9 -`, starting
  with a letter or digit.
- **`kill`:** needs no password. It fails claimed and running inbound tasks
  with note `killed`, tries for up to 3 seconds to send those updates, stops
  file downloads, disconnects from the relay, stops handling incoming
  messages, and persists across restarts. Updates not sent yet go out after
  `resume`. While the switch is on, every method outside the kill-safe list
  fails with `killed`, including `chat.send`, `task.*` and `file.send`, and
  calling `kill` again fails with `killed`.
- **`pair.start`:** needs a live relay connection (`offline` otherwise).
  `pair.await` blocks until the joiner finishes the exchange (at most 10
  minutes). `pair.finalize` needs `trust` and a valid `alias`; on a machine
  without a relay mailbox it registers one with the invite received while
  pairing. A successful exchange gets a fresh 10 minutes to finalize.
- **`approvals.list`:** oldest first. `preview` is the first 500 characters,
  `sha256` the hex SHA-256 of the full text, `full` the full text.
- **`approvals.decide`:** approving queues the task, tells the sender, and
  delivers it to the sessions. Approving checks the peer again: it fails with
  `not_found` when the peer was unpaired, `paused` when this machine paused it,
  and `not_permitted` when its trust no longer allows tasks. Denying rejects
  the task and tells the sender (note `denied by the receiving human`). A task
  that is no longer awaiting approval fails with `bad_transition`.
- **`files.accept`:** only for inbound files `held` from a chat-only peer
  (`bad_transition` otherwise), and not while this machine has the peer paused
  (`paused`) or its trust no longer allows files. The quota and disk checks
  still apply; if one fails the file is declined, the sender is told, and the
  error is returned (`quota` for the quota).
- **`reset_identity`:** only runs while the kill switch is off. It turns the
  kill switch on, deletes every peer, prekey and outbox item, stops pending
  pairings, and creates a new identity with no relay mailbox.
- **`audit.read`:** the newest `limit` events (default 50, at most 1000),
  oldest first.
- **`hook.counts`:** counts unread items for the session registered in `cwd`
  (or machine-wide items no session has read), keyed by local alias, plus
  pending approvals. `notice` is a ready line such as
  `cravv-connect: 2 new messages from gpu-box, 1 task awaiting your approval. Use check_inbox.`
  (with only approvals pending it ends `Run cravv-connect approvals in your terminal.`)
  and is empty when there is nothing. `unread` is the total unread count. It never contains message bodies or
  names chosen by a peer.

## 6. Views

```jsonc
// InboxView
{"seq": 12, "id": "01J...", "from": "gpu-box", "session": "codex@training",
 "kind": "chat",
 "wrapped": "<remote_message from=\"gpu-box\" ...>...</remote_message>",
 "at": "2026-09-26T10:00:00Z"}
```

- `kind` is `chat`, `task`, `task_update`, or `file`.
- `path` is set for a downloaded file.
- `wrapped` is the only field agents should read as content:
  `<remote_message from="alias" session="..." trust="..." id="..." kind="..." task_id="...">body</remote_message>`
  with `session` and `task_id` omitted when empty, attribute values stripped
  of control and invisible characters, capped at 64 characters and
  XML-escaped, and a body on its own line with invisible characters removed
  and `&`, `<`, `>` escaped, so the body cannot close the tag.

```jsonc
// TaskView
{"task_id": "01J...", "direction": "in", "peer": "gpu-box", "state": "running",
 "claimed_by": "codex@training", "result": "", "instructions": "...",
 "notes": [{"at": "2026-09-26T10:00:00Z", "text": "halfway"}],
 "files": [], "result_files": [], "updated_at": "2026-09-26T10:00:00Z"}

// PeerView
{"alias": "gpu-box", "machine_id": "52 chars", "trust_in": "ask-first",
 "online": true, "paused": false, "paused_by_peer": false, "paired_at": "..."}

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
 "sessions": ["claude@glow-v2"], "outbox_pending": 0, "outbox_held": 0,
 "inbox_unread": 3, "pending_approvals": 1}

// Event (audit.read)
{"ts": "...", "type": "pair", "peer": "machine id", "alias": "gpu-box",
 "detail": {"trust": "ask-first", "role": "creator"}}
```

Empty optional fields are omitted: `session`, `task_id`, `file_id`, `path`
(InboxView); `claimed_by`, `result`, `notes`, `files`, `result_files`
(TaskView); `path`, `reason` (FileView); `errors` (StatusResult); `peer`,
`alias`, `item_id`, `hash`, `detail` (Event).

`trust_in` is what that peer may do on this machine. A peer is `online` when
the relay connection is up, neither side has paused the other, and the peer
sent this machine something (including delivery receipts) in the last 10
minutes. File `state` is one of `held`, `downloading`, `done`, `failed`,
`declined` (inbound) or `uploading`, `sent`, `failed` (outbound).
`outbox_pending` counts pending and queued items. `sessions` lists connected
sessions, and `inbox_unread` is summed over them.

## 7. Error kinds

| Kind | Meaning |
|---|---|
| `not_found` | Unknown alias, task, file, or pending pairing |
| `not_permitted` | Not allowed for this session or trust level |
| `paused` | This machine paused the peer |
| `paused_by_peer` | The peer paused this machine (reserved: current methods hold such messages instead of failing) |
| `killed` | The kill switch is on |
| `auth_required` | The method needs `auth.unlock` on this connection |
| `locked` | Too many wrong passwords; wait 15 minutes |
| `bad_password` | Wrong password |
| `already_claimed` | Another session claimed the task |
| `bad_transition` | The task or file is not in a state that allows this |
| `too_large` | Text over 64 KiB (65536 bytes); files over 100 MiB are `path_refused` |
| `path_refused` | The file may not be sent (see `file.send`) |
| `quota` | The peer's inbound file quota is used up |
| `no_session` | The method needs `session.register` first |
| `bad_request` | Invalid params (also an invalid alias) |
| `internal` | Anything else (for example an alias already in use, a malformed bind code, low disk space, or a folder that does not exist) |

Kinds registered by the daemon (`internal/app`) on top of these:

| Kind | Meaning |
|---|---|
| `offline` | No live relay connection (for example `pair.start`), or the pairing service stopped (daemon shutdown or identity reset) |
| `pairing_failed` | Wrong code, burned room, or an interrupted or tampered exchange |
| `pairing_expired` | The exchange did not finish within 10 minutes, or it finished more than 10 minutes before `pair.finalize` |
| `busy` | `pair.finalize` before the exchange finished |
| `auth_unavailable` | No PAM (built without cgo), a `pam_service` that is not on the allowlist, or a PAM stack that accepted a random password |

Clients map kinds back to the Go sentinel errors (`errors.Is` works on the
client side); a kind the client does not know keeps its `kind` and `message`.
