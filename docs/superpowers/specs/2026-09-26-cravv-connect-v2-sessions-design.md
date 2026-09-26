# cravv-connect v2: Session Links, Chat Hub, Managed Sessions, Simple Setup, UI

- **Date:** 2026-09-26
- **Status:** Approved design, revised after Fable review
- **Builds on:** `2026-09-26-cravv-connect-design.md` (v1). This spec only describes what changes; everything in v1 stays unless replaced here.

## 1. Why

The first real trial between a Mac and a Linux GPU box worked, but it was not seamless. The problems it showed:

1. **Receiving was manual.** An agent only saw new items while a human had told it to "listen in a loop", and any human input stopped the loop.
2. **Nothing arrived on its own.** Replies and incoming tasks were seen only when the human asked "anything?".
3. **Tasks were approved twice.** Once in the CLI, then again when the agent asked.
4. **Claude Code's own permission checks got in the way.** Auto mode blocked `complete_task`, the sender waited with no signal, and the human had to retry twice.
5. **Waiting was clunky.** Waits were capped at 50 seconds, and a stuck peer looked the same as a slow one.
6. **Authorization was machine-wide.** Any agent session on a trusted machine could act on any task. The user wants the unit of trust to be a session.
7. **Setup took many manual steps.** The second machine had to install Go. Relay addresses had to be retyped on every IP change. There was no overview of devices and sessions.

## 2. Goals and success criteria

1. **The chat is the hub.**
   - Everything addressed to a session shows up in that session's chat without the human asking, including while the chat is idle.
   - Accepting and rejecting happens in the chat for everything except the few actions that need a password (section 10).
2. **Session-to-session links.**
   - Two sessions can only exchange messages, tasks or files over an accepted link.
   - A session may hold several links.
   - Traffic on one link is never visible to another session.
3. **Presence.**
   - When a linked session closes, the other side learns within 5 seconds (both machines online), or within 150 seconds when a machine drops.
   - A session that is only briefly away (sleep, MCP reconnect) does not lose its links.
4. **Existing or new.**
   - A host can link to a session the remote side already has open.
   - A host can also ask the remote daemon to start a managed session in a folder the remote owner pre-approved. This works with no human at the remote machine.
5. **One approval.**
   - A link request is decided once, on the accepting side.
   - A task under an "ask each time" link is decided once, on the receiving side.
   - The sender always sees a task's state, including `seen`, so "stuck" and "slow" look different.
6. **Setup:**
   - A fresh machine goes from nothing to paired and chat-connected with an install one-liner plus one command (`cravv-connect setup`, or `setup --join <code>`).
   - No Go toolchain is needed.
7. **UI.** Devices, sessions, links and managed-session rules can be seen and changed both from the chat and from a local web page.
8. **Security.**
   - Pairing, broad grants and managed-session rules keep the v1 password gate.
   - The kill switch, pause and unpair keep working.
   - The relay stays blind.
   - Session identity cannot be taken over by local processes that were not given it.

## 3. Concepts

### 3.1 Machine

Machines work as in v1: identity keys, one-time pairing with a bind code and the login password, and a relay mailbox.

Pairing now only authorizes **discovery** (listing visible sessions) and **link requests**. It grants nothing that reaches an agent.

The v1 per-machine trust levels (`chat-only`, `ask-first`, `autonomous`) are **removed**.
- **On upgrade:** pairings are kept but carry no links.
- **Link-less v1 traffic:** `chat`, `task.*` or `file.offer` arriving without a `link_id` gets a `control.unsupported{min_version: 2}` reply (rate-limited to one per peer per hour), and the item is dropped. The v1 sender shows "peer needs upgrade". This lets the other machine know it has to upgrade.

The v1 `--json` agent CLI commands (`send`, `inbox`, `wait`, `task ...`) are replaced by link-scoped equivalents that act on a shared session through the same connection-bound identity (section 3.2).

### 3.2 Session

A session is a named endpoint on one machine: `<machine-alias>/<session-name>`, such as `gpu-box/trainer`.

| Field | Meaning |
|---|---|
| `session_id` | Core ID, generated at creation. Stable for the session's lifetime. Never shown on a command line (below). |
| `name` | Unique per machine among open sessions. Lowercase `[a-z0-9-]`, at most 32 characters. |
| `purpose` | One line, at most 120 characters, shown to peers that can see the session. |
| `kind` | `live` (a human's chat) or `managed` (started by the daemon) |
| `agent` | `claude`, `codex`, `cli`, and so on |
| `project_dir` | Local only; never sent to peers |
| `visibility` | `private` (default), `peers:[machine ids]`, or `all-peers` |
| `state` | `open`, `away` or `closed` |

#### Identity is bound to a connection, never to an argument

- **Sharing.** A live session is created when an agent chat calls `session_share` over its MCP connection. The daemon binds the session to that IPC connection (v1 `ConnState`). Every link tool derives the session from the connection, and no tool takes a session ID as input.
- **Reattach.** Another connection can take over a session only by presenting its **reattach token**. The token is a random secret the daemon gives the MCP server at share time and never writes to a command line or to disk outside the daemon's 0600 store. The MCP server keeps it in memory and uses it to reattach after a daemon restart or an MCP reconnect. A reattach must also come from the same agent and project folder.
- **Listener.** The listener (section 7.1) gets a **wake token**. It lets the listener learn "something is pending for this session" (counts only) and nothing else. The MCP server hands it to the agent in the `session_share` result, and the agent passes it to `cravv-connect listen` on **stdin**, never as an argument.
- **Managed runs.** A managed child run gets a **run token** in an environment variable. The daemon maps it to exactly one managed session for one run, and revokes it when the run ends.

#### Lifecycle

- **Away.** A live session whose connection disappears goes to `away`. Peers see "away". Sends to it queue for up to the **away grace of 10 minutes**, and its links stay open.
- **Close.** It becomes `closed` on `session_close`, or when the away grace runs out, and all its links close. Reattaching during `away` returns it to `open` and delivers what queued.
- **Unshared connections** are "attachments" (the v1 session registry). They can use discovery and management tools but cannot send or receive link traffic.
- **Hooks.** Claude Code hooks identify their session through the `session_share` result, which the MCP server records against Claude Code's own session ID (passed to hooks as `session_id`). Two chats in one folder are no longer confused.

### 3.3 Visibility and discovery

- **Listing.** `sessions.list` from a paired machine returns only open or away sessions whose visibility includes the asker (name, purpose, kind, agent, state), plus the managed-session offers made to that asker (labels only).
- **Unseen sessions.** A session the asker can't see answers exactly like a missing one (`not_found`).

### 3.4 Link

A link connects two sessions on two different machines.

- **Identity.**
  - It is identified by a `link_id` (core ID) that the requester mints. Each side stores it keyed by `(remote machine, link_id)`.
  - On all link traffic, the receiver derives the sender's session only from its own link record. It ignores any `from_session` in the body.
- **Permission.** Each side sets `permission_in`, meaning what the other side may do to it:
  - `messages`: chat and files within the link.
  - `tasks-ask`: messages, plus tasks that each need a human decision on this side.
  - `tasks-auto`: messages, plus tasks the agent may carry out without asking.
- **Setting and changing permission.**
  - The requester proposes a level. The acceptor accepts, lowers or rejects.
  - Lowering is always allowed and needs no gate.
  - Raising, or granting `tasks-auto` at accept time, needs the password (section 10).
- **Lifecycle.**
  - States are `pending`, then `active`, then `closed`.
  - A link closes when either session closes, when either side calls `disconnect`, on pause or unpair of the machine, on the kill switch, or on presence timeout.
  - Closed links are never reopened.
- **On close:**
  - Tasks in `queued`, `claimed` or `running` state on that link become `failed: link_closed`, and the sender is told when reachable.
  - Items queued for an away session are dropped, with a failure notice to their sender.

## 4. Protocol changes (peer-v1 kinds; the relay protocol is unchanged)

These are new envelope kinds, sealed as in v1. The v1 kinds `chat`, `task.*` and `file.offer` gain a required `link_id`. The receiver drops them, and answers `link.closed` (rate-limited to 1 per link per minute), unless all of these hold:
- the link is `active` (or `away` on this side);
- the sender machine is the link's remote end;
- the local link's `permission_in` allows the kind.

| Kind | Body | Notes |
|---|---|---|
| `sessions.list` | `{req_id}` | Receiver limit: 30 per peer per minute |
| `sessions.listed` | `{req_id, sessions:[{session_id,name,purpose,kind,agent,state}], offers:[{offer_id,label,agent,max_permission}]}` | Visible entries only |
| `link.request` | `{link_id, from_session:{id,name,purpose}, to_session_id \| offer_id, proposed_permission, note}` | `note` is at most 280 characters, shown wrapped. Receiver limit: at most 5 pending requests per peer; extras are rejected `busy`. |
| `link.accepted` | `{link_id, to_session:{id,name,purpose}, granted_permission}` | |
| `link.rejected` | `{link_id, reason}` | `reason` is one of `declined`, `not_found`, `busy`, `policy`, `timeout` |
| `link.closed` | `{link_id, reason}` | `reason` is one of `closed_by_peer`, `session_closed`, `paused`, `unpaired`, `killed`, `presence_timeout`, `unknown_link` |
| `link.state` | `{link_id, state: active \| away, permission_in}` | Informational; enforcement is local |
| `presence.ping` | `{ts, link_ids}` | Sent directly, not through the outbox |
| `presence.pong` | `{ts, link_ids_open}` | Sent directly, not through the outbox |
| `control.unsupported` | `{min_version}` | Reply to link-less v1 traffic |
| `task.update` state `seen` | | Sent when the receiving session's `check_inbox` first returns the task |

**Pending requests.** A request pending for 10 minutes is rejected with `timeout`. The limits in the table above are enforced by the receiver.

**Delivery for presence frames.**
- They are sent directly: never queued in the outbox, never receipted, never retried.
- A receiver drops any that are older than 120 seconds (by `ts`).
- Frames the relay queued while a machine was offline therefore expire harmlessly.

## 5. Presence

1. **Local close.** When a session closes locally, the daemon immediately sends `link.closed` for each of its links, through the outbox so it survives a brief disconnect.
2. **Away.** When a session goes away or comes back, the daemon sends `link.state` to each linked peer.
3. **Heartbeat.**
   - While at least one link to a peer is open, each daemon sends `presence.ping` every 30 seconds.
   - A link counts as dead after 150 seconds without a fresh pong, or as soon as a fresh pong leaves it out.
   - Either way it becomes `closed(presence_timeout)` on that side, and the session is told.
4. **Split brain.** If one side considers a link closed and the other still sends on it, the `link.closed{unknown_link}` reply converges them.
5. **Offline sends.** Sending on a link that is not active (or away) returns `link_closed` at once.

## 6. Managed sessions

### 6.1 Offer rules

Offer rules are set per peer machine and are **password-gated** (web UI or CLI).

| Field | Meaning |
|---|---|
| `label` | |
| `folder` | Absolute path. Must not be `$HOME` or contain `~/.cravv-connect`. It is re-checked on every run (it must still exist, not be a symlink that moved, and stay inside the allowed tree). |
| `agent` | `claude` in v2. Adapters are behind an `AgentAdapter` interface. |
| `permission` | `messages` or `tasks-auto`. `tasks-ask` is not offered, because a managed session has no human to ask. |
| `run_mode` | See below |
| `max_concurrent` | Default 2 |
| `idle_timeout` | Default 2 hours |
| `max_turns_per_run` | Default 40 |
| `run_timeout` | Default 30 minutes |
| `runs_per_hour` | Default 30 per link |
| `runs_per_day` | Default 200 per machine |

`run_mode` values:
- `read-only`: `--allowedTools Read,Glob,Grep,mcp__cravv-connect__*`
- `edit-in-folder`: `--permission-mode acceptEdits --disallowedTools Bash,WebFetch,WebSearch`
- `shell`: acceptEdits plus Bash.
  - The rule editor makes the human type `shell` and shows "The peer can run commands as your user on this machine".
  - This mode is what running training jobs needs.

Every run also:
- uses `--strict-mcp-config` with a daemon-written MCP config that contains only cravv-connect (so no user-scope MCP servers or hooks are inherited);
- sets `--disallowedTools "Bash(cravv-connect:*)"`, so the child cannot drive the local cravv-connect CLI;
- never uses a bypass permission mode.

### 6.2 Creation and runs

- **Creation.** A `link.request` to an offer:
  1. checks the rules, the caps and the concurrency limit;
  2. creates a managed session named from the label plus a 4-character suffix;
  3. accepts at `min(proposed, permission)` with no prompt. The password-gated rule was the human's approval.
- **Runs.**
  - The daemon's `SessionHost` keeps one queue per managed session and runs one item at a time.
  - Each item runs through the adapter: `claude -p <prompt> [--resume <agent_session_id>] --output-format json` in the folder, with the flags above. If the agent CLI supports choosing a session ID up front (`--session-id`, checked in Phase 0), the daemon uses it; otherwise it records the ID from the first run's JSON.
  - The child's MCP server identifies itself with the run token. It can only act as that managed session, and only within the link's permissions. It cannot share sessions, connect, or change policy.
- **Opening.** `cravv-connect session open <name>` (or "Open" in the web UI) runs `claude --resume <id>` in the folder. While a human has it open, the daemon pauses the session's queue and the session shows as `live`.
- **Audit and limits.** Every run is audited (session, link, duration, turns, exit status). Hitting a cap fails the task with `rate_limited` and tells the sender.
- **Links per managed session.** A managed session has exactly one link. Closing that link closes the session.

## 7. Chat hub

### 7.1 Wake-up

- **Listener (primary for Claude Code).** A shared session keeps a background listener, `cravv-connect listen`, with the wake token on stdin.
  - It blocks until the session has something new.
  - It prints one line that names only the local alias and a link number (`cravv-connect: 1 new task on link 2 from gpu-box. Call check_inbox.`) and exits.
  - Claude Code wakes an idle chat when a background command exits. The `/cravv` skill and the MCP instructions tell the agent to start the listener after sharing and again after every wake.
- **Missed re-arm.** If the agent forgets to start the listener again, the Stop hook and the UserPromptSubmit notice remind it, so delivery degrades to "next turn" rather than silence.
- **Phase 0 also tests** two other wake paths:
  - MCP channels;
  - server-initiated notifications and elicitation outside a tool call.

  Whichever works reliably becomes primary, with the listener as the fallback.
- **Stop hook.** When the chat's session has *unhandled* items (delivered to the session but not yet returned by `check_inbox`), the hook blocks with a one-line reason.
  - It never blocks for approvals only.
  - It respects `stop_hook_active`.
  - It stops after 2 blocks in a row.
- **Other agents** (Codex and others) poll with `wait_for_message(timeout_s)`. The default is 50 seconds, and `timeout_s` can be raised up to 600 seconds for clients with longer tool timeouts.

### 7.2 Decisions in chat

- **`review_pending()`.**
  - For each pending decision it opens one MCP **elicitation** form: requester (local alias and remote session name), purpose or note (wrapped), permission asked, and the choices.
  - A second call while a form is open does not open another.
  - Answers are cached by item ID and are single-use.
  - Calls are limited to 6 a minute.
- **What an elicitation can decide:**
  - accept or reject a link at `messages` or `tasks-ask`;
  - approve or reject a single `tasks-ask` task;
  - lower a permission.
- **What it cannot decide** (it offers a password path instead):
  - accept at `tasks-auto`;
  - raise a permission;
  - edit offer rules;
  - pair or unpair.

  For those, the form says "needs your password: run `cravv-connect approve <id>` or open the UI". This is the tiered gate from section 10.
- **After approval.** A task accepted through elicitation is not asked about again. The MCP instructions state that accepted tasks were approved by the human.
- **Fallback** when the client can't show elicitation: the item stays pending, and `review_pending` returns the CLI and UI paths.

### 7.3 MCP tools (replacing the v1 set)

| Tool | Purpose |
|---|---|
| `session_share(name, purpose, visibility?)` | Share this chat. Returns the wake token and listener instructions. |
| `session_close()` | Close this chat's session and its links |
| `session_set(visibility?, purpose?)` | Change visibility or purpose |
| `machines()` | Paired machines and whether they are online |
| `sessions(machine)` | A machine's visible sessions and offers |
| `connect(target, permission, note?)` | `target` is `machine/session` or `machine/new:<offer label>` |
| `links()` | This session's links, peers, permissions and presence |
| `disconnect(link)` | Close a link |
| `restrict(link, permission)` | Lower what the peer may do |
| `check_inbox(limit?)` | This session's items only |
| `wait_for_message(timeout_s?)` | For agents without the listener |
| `review_pending()` | Opens the elicitation forms |
| `send_message(link, text)` | |
| `create_task(link, instructions, file_paths?)` | |
| `get_task` / `claim_task` / `update_task` / `complete_task` / `fail_task` / `cancel_task` | Scoped to this session's links |
| `send_file(link, path)` | Same path rules as v1 |
| `kill_switch()` | |

Machine management (pause, resume, unpair, alias) moves to the CLI, the web UI and a `cravv-connect` command. The agent-reachable cut-off tools are `disconnect`, `restrict` and `kill_switch`.

### 7.4 Claude Code permissions

- **Allow rules by default.** Setup adds allow rules for every tool that only acts within an existing link, or only reads:
  - `machines`, `sessions`, `links`, `check_inbox`, `wait_for_message`, `review_pending`;
  - `session_share`, `session_close`, `session_set`, `disconnect`, `restrict`;
  - `send_message`, `get_task`, `claim_task`, `update_task`, `complete_task`, `fail_task`, `cancel_task`;
  - `Bash(cravv-connect listen:*)`.

  This is what removes the trial's `complete_task` block. The link is the boundary.
- **Prompted by default:** `connect`, `create_task` and `send_file`. They open new flows or send local files. Setup asks whether to allow them too.
- **Uninstall.** The settings merge is idempotent and reversible with `uninstall claude`.

## 8. Setup

- **Release binaries.**
  - A GitHub Actions workflow builds `cravv-connect` for darwin-arm64, darwin-amd64, linux-amd64 and linux-arm64 (native runners, cgo for PAM), plus `cravv-relay` (no cgo), with checksums.
  - `install.sh` detects the OS and architecture, downloads, verifies and installs to `~/.local/bin`.
  - This needs the repo on GitHub; the module path is fixed then.
- **`cravv-connect setup` (host), guided:**
  1. Relay URL, or offer to start a LAN test relay.
  2. Admin token if this is the relay's first machine.
  3. `init` and `daemon install`, then wait until the daemon is online.
  4. Detect `claude` and `codex`, and install their integrations (MCP server, hooks, the `/cravv` skill, allow rules).
  5. Offer "pair a device now", which shows a **join code** and waits.
- **Join code.** `cravv-join:<base32(relay origin)>:<nameplate>-<secret>`, also shown as a terminal QR code.
- **`setup --join <code>` (other machine).**
  - It decodes the relay origin and shows it: "Join relay https://... ? (y/N)".
  - It refuses plain http unless the address is a loopback or private network address.
  - It refuses if this machine is already set up for a different relay; that takes an explicit `setup --reset`.
  - Then it runs init, daemon install, join, the alias prompt and the agent integrations.
- **LAN relays.** `setup` suggests `<host>.local` over a raw IP when it resolves.

## 9. Web UI

- **Launch.**
  - `cravv-connect ui` asks the daemon to start an HTTP server on `127.0.0.1` with a random port, and opens the browser.
  - The URL carries a one-time launch token. It is swapped for an `HttpOnly`, `SameSite=Strict` cookie and followed by a redirect that removes the token from the URL.
- **Request checks.**
  - Every state-changing request needs the cookie, a CSRF token, and an `Origin` matching the UI origin.
  - `Host` must be `127.0.0.1:<port>` or `localhost:<port>`, which blocks DNS rebinding.
  - Responses are `Cache-Control: no-store`.
- **Build.** The UI is embedded HTML, JS and CSS with no build step. It is a thin client over the existing IPC ports, not a second API.
- **Pages:**
  - **Devices:** online status, last seen, pair a new device (join code and QR), pause, resume, unpair.
  - **Sessions:** local sessions and each device's visible ones, links, connect, disconnect, restrict.
  - **Managed rules and sessions:** open and close.
  - **Approvals.**
  - **Activity:** the audit tail.
- **Gating.**
  - The same tiers as section 10.
  - Password actions ask for the login password on the page, checked by the daemon's Guard with the same lockout.
  - The server stops 30 minutes after the last request, or with the daemon.
- **Honest limits (documented).**
  - Any local process running as the same user can run `cravv-connect ui` or the CLI and use the actions that need no password, exactly as with the v1 CLI.
  - Wrong passwords from any local process count toward the 15-minute lockout.

## 10. Security

### Enforcement

The daemon's `PolicyGate` checks the link, not the machine:
- the link exists and is active or away;
- the sender is its remote machine;
- the local session is its local end;
- `permission_in` allows the kind.

Inbox, tasks and files are scoped by `(session_id, link_id)`.

### Tiered human decisions

| Decision | Gate |
|---|---|
| Accept a link at `messages` or `tasks-ask`; approve a single `tasks-ask` task; lower anything | Elicitation in chat, or password in the CLI or UI |
| Accept or raise to `tasks-auto`; edit offer rules; pair or unpair; raise a machine-level setting | Password (CLI or UI) only |

**What elicitation protects against.** It is human-only relative to the model in that chat. It is **not** proof against other local processes running as the same user, which could drive their own MCP client. The damage such a process can do is bounded: it can accept a link or a single task, never grant automatic execution or edit rules. The docs state this plainly.

### Other protections

- **Identity:** connection binding, reattach, wake and run tokens (section 3.2). No session ID ever appears in `ps`.
- **Managed runs:** the section 6 restrictions (strict MCP config, run mode tool rules, CLI denied, caps, folder checks).
- **Threat-model additions to document:**
  - a managed `shell` session lets the peer act as your user;
  - on Linux, the identity seed in `store.db` is readable by same-user processes (v1 already says so).

## 11. Phases

0. **Feasibility check** on the real Mac (CLI and VS Code) and the GPU box:
   - Does a background command exit wake an idle session, in each client?
   - Channels.
   - Server-initiated notifications and elicitation outside a tool call.
   - Elicitation in default, acceptEdits, auto and dontAsk modes. Can the model answer it?
   - `claude -p --resume` continuity.
   - `--session-id` support.
   - Interactive resume of a headless session.

   The results fix section 7.1 and the fallbacks in 7.2.
1. **Sessions, visibility, links, presence.** Store, daemon, protocol kinds, identity tokens, policy gate, removal of v1 trust levels, `control.unsupported`.
2. **Chat hub.** Listener, hooks, `review_pending` with tiered gating, new MCP tools, `task.update: seen`, allow rules, the `/cravv` skill.
3. **Managed sessions.** Offer rules, `SessionHost`, `AgentAdapter` (Claude), run flags and caps, open.
4. **Setup.** The wizard, join codes, the release workflow and `install.sh`.
5. **Web UI.**
6. **E2E and docs.** The new model end to end, and updated README, security and protocol docs.
