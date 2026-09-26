# Connecting your coding agent

cravv-connect talks to agents through a stdio MCP server, `cravv-connect mcp`.
Every agent that can start an MCP server can use it. A chat takes part by
sharing itself as a session; sessions on paired machines then ask to link
with it, and everything travels over those links.

Before you start, set the machine up with `cravv-connect setup` (or
`cravv-connect setup --join <code>` on a second machine): it installs the
daemon and offers the Claude Code and Codex integrations below. Use the full
path of the binary in the snippets below if it is not on the `PATH` your
agent sees; `which cravv-connect` prints it.

The MCP server takes its project folder from the directory the agent starts
it in. If your editor starts MCP servers somewhere else, add
`"--project-dir", "<absolute project path>"` after `"mcp"`; VS Code and
Cursor accept `${workspaceFolder}` there. Files a chat sends must be inside
that folder or a folder you allowed with `cravv-connect allow-path`.

## The tools

| Tool | What it does | Claude Code, after `install claude` |
|---|---|---|
| `session_share(name, purpose, visibility?)` | Share this chat as a session; returns the listener command | allowed |
| `session_close()` | Close this chat's session and all its links | allowed |
| `session_set(purpose?, visibility?)` | Change the purpose or who may see it (`private`, `all-peers`, `peers:<alias>,...`) | allowed |
| `machines()` | Paired machines, online or paused | allowed |
| `sessions(machine)` | The sessions and managed-session offers a machine shows this one | allowed |
| `connect(target, permission, note?)` | Ask `machine/session`, or `machine/new:<offer label>`, for a link; the other side decides | asks |
| `links()` | This session's links: peers, permissions, state, presence | allowed |
| `disconnect(link)` | Close a link | allowed |
| `restrict(link, permission)` | Lower what the other side may do here | allowed |
| `check_inbox(limit?)` | What arrived for this session, wrapped in `<remote_message>` | allowed |
| `wait_for_message(timeout_s?)` | Block until something arrives (default 50 seconds, at most 600) | allowed |
| `review_pending(item?, decision?, code?)` | Ask your human to decide link requests and held tasks | allowed |
| `send_message(link, text)` | Chat on a link | allowed |
| `create_task(link, instructions, file_paths?)` | Give the other session a task | asks |
| `get_task`, `claim_task`, `update_task`, `complete_task`, `fail_task`, `cancel_task` | Work on tasks; peer text comes only in the `wrapped` field | allowed |
| `send_file(link, path)` | Send a file from the project folder (up to 100 MB; hidden and secret files refused) | asks |
| `kill_switch()` | Stop everything until the human resumes with the password | asks |

`permission` is `messages` (chat and files), `tasks-ask` (tasks that each
need a human decision on the other side) or `tasks-auto` (tasks the other
agent may run without asking). Machine management (pairing, pausing,
unpairing, offers) is for the human, in the CLI or the web UI.

## Claude Code

### Install

```sh
cravv-connect install claude            # or: cravv-connect setup
cravv-connect install claude --allow-send
```

This:

- registers the MCP server: `claude mcp add --scope user cravv-connect -- <bin> mcp`;
- adds the UserPromptSubmit and Stop hooks (`<bin> hook`, 5 second
  timeout) to `~/.claude/settings.json`;
- adds allow rules to `permissions.allow` there for every tool marked
  "allowed" above, plus `Bash(cravv-connect listen:*)` and
  `Bash(<bin> listen:*)` (the listener command uses the plain name when
  the `PATH` finds this binary, else its full path), so the listener and the tools that only read or act within an existing link
  never stop the chat for a permission prompt;
- writes the `/cravv` skill to `~/.claude/skills/cravv/SKILL.md`.

`connect`, `create_task` and `send_file` open new flows or send local files,
so they still ask. `--allow-send` allows them too; `--no-allow-send` removes
the rules `--allow-send` added. Running it again without either flag keeps
the send rules as they are. Rules and skills you wrote yourself are never
changed, and `cravv-connect uninstall claude` removes exactly what it added.
Restart Claude Code afterwards.

Manual equivalent:

```sh
claude mcp add --scope user cravv-connect -- /usr/local/bin/cravv-connect mcp
```

and in `~/.claude/settings.json`:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [ { "type": "command", "command": "/usr/local/bin/cravv-connect hook", "timeout": 5 } ] }
    ],
    "Stop": [
      { "hooks": [ { "type": "command", "command": "/usr/local/bin/cravv-connect hook", "timeout": 5 } ] }
    ]
  },
  "permissions": {
    "allow": [
      "mcp__cravv-connect__machines", "mcp__cravv-connect__sessions", "mcp__cravv-connect__links",
      "mcp__cravv-connect__check_inbox", "mcp__cravv-connect__wait_for_message", "mcp__cravv-connect__review_pending",
      "mcp__cravv-connect__session_share", "mcp__cravv-connect__session_close", "mcp__cravv-connect__session_set",
      "mcp__cravv-connect__disconnect", "mcp__cravv-connect__restrict", "mcp__cravv-connect__send_message",
      "mcp__cravv-connect__get_task", "mcp__cravv-connect__claim_task", "mcp__cravv-connect__update_task",
      "mcp__cravv-connect__complete_task", "mcp__cravv-connect__fail_task", "mcp__cravv-connect__cancel_task",
      "Bash(cravv-connect listen:*)", "Bash(/usr/local/bin/cravv-connect listen:*)"
    ]
  }
}
```

### Share a chat: `/cravv`

Type `/cravv` in a chat (or ask "share this chat with cravv-connect"). The
skill asks for a short name (`a-z`, `0-9`, `-`), a one-line purpose and who
may see it, then:

1. calls `session_share`, which returns a listener command such as
   `cravv-connect listen --wake-file /Users/you/.cravv-connect/wake/wake-<32 hex>`;
2. runs it as a background command (the Bash tool with
   `run_in_background`). The command names a private file that holds the
   wake token; the token itself never appears in the command, the chat
   transcript or `ps`.

The listener blocks until something arrives for this chat's session, prints
one line naming only the local machine alias and link number, and exits:

```text
cravv-connect: 1 new task on link 2 from gpu-box. Call check_inbox.
cravv-connect: 1 link request on link 3 from laptop. Call check_inbox, then review_pending.
```

Claude Code wakes an idle chat when a background command exits, so the
chat handles what arrived without you asking: `check_inbox`, then
`review_pending` when the line says so, then it starts the listener again.
If the chat forgets to re-arm it, the prompt hook adds "The listener for
this chat's session is not running" to your next prompt, and the Stop hook
keeps the chat going while something unhandled is waiting, so delivery
falls back to "next turn", never to silence.

To link with a session on another machine, ask in plain words ("connect to
gpu-box/trainer so it can run tasks for us"); the chat calls `sessions` and
`connect`. The human on that machine decides.

### Accept and reject in the chat

Link requests to this chat and tasks on its `tasks-ask` links wait for you.
The chat calls `review_pending`, and:

- **In a terminal (Claude Code CLI):** a form appears in the chat. Choose
  accept, a lower level, or reject. The model never answers it; a form you
  dismiss decides nothing.
- **In the VS Code extension:** the extension declines forms without
  showing them, so the daemon shows a desktop notification on macOS with a
  4-digit code. Type `accept 4821` (or `reject`) in the chat; the chat
  passes what you typed to `review_pending`. The model never sees the
  code. A code works once, for 10 minutes, and 3 wrong ones lock that item.
- **Where neither works** (Linux has no notifier): `review_pending` names
  the password path, `cravv-connect link accept <link>` or
  `cravv-connect approvals` in a terminal, or the web UI
  (`cravv-connect ui`).

A chat can accept a link at `messages` or `tasks-ask` and approve single
tasks. Granting `tasks-auto`, raising a link and editing offers always need
your password (`cravv-connect link accept`, `link permit`, or the web UI).
Tasks that reach the chat were approved by a human (or arrived on a
`tasks-auto` link), and the MCP instructions tell the agent not to ask
again.

### Hooks

- **UserPromptSubmit** prints one line such as
  `cravv-connect: 2 new messages on link 2 from gpu-box. Call check_inbox.`
  and Claude Code adds it to your prompt.
- **Stop** answers `{"decision":"block","reason":"<the same line>"}` when
  this chat's session has unhandled items, so Claude takes one more turn.
  It never blocks for decisions alone (only you can make those), not again
  for the same items while `stop_hook_active`, and at most twice in a row.
- Both find the chat's own session through Claude Code's chat ID
  (`CLAUDE_CODE_SESSION_ID`, given to the MCP server, and `session_id`,
  given to hooks), so two chats in one folder are not confused. After
  `/clear` the chat ID changes but the MCP server keeps the old one, so
  the hooks stay silent for that chat; the listener keeps working. A hook
  whose chat ID is unknown answers for the newest open session of its
  folder that no chat claims.
- They never print message bodies or names chosen by the other machine,
  and print nothing when the daemon is not running.

## Codex

```sh
cravv-connect install codex             # or: cravv-connect setup
```

This adds (or rewrites) one table in `~/.codex/config.toml` and leaves the
rest of the file alone:

```toml
[mcp_servers.cravv-connect]
command = "/usr/local/bin/cravv-connect"
args = ["mcp"]
```

Codex has no hooks and no `/cravv` skill. Ask it to share the chat
(`session_share`), then to listen with `wait_for_message`, calling it again
while you wait. The default wait is 50 seconds, which fits clients whose
tool calls time out after a minute; pass `timeout_s` up to 600 only if your
client allows longer tool calls. For decisions it calls `review_pending`:
if the client shows no form, you get the code notification on macOS;
otherwise decide in the web UI (`cravv-connect ui`, Approvals page) or the
terminal.

## Cursor

Project level, `.cursor/mcp.json` (or `~/.cursor/mcp.json` for every project):

```json
{
  "mcpServers": {
    "cravv-connect": {
      "command": "/usr/local/bin/cravv-connect",
      "args": ["mcp", "--project-dir", "${workspaceFolder}"]
    }
  }
}
```

## VS Code (GitHub Copilot agent mode)

`.vscode/mcp.json` in the workspace. Note the top-level key is `servers`:

```json
{
  "servers": {
    "cravv-connect": {
      "type": "stdio",
      "command": "/usr/local/bin/cravv-connect",
      "args": ["mcp", "--project-dir", "${workspaceFolder}"]
    }
  }
}
```

## Gemini CLI

`~/.gemini/settings.json` (or `.gemini/settings.json` in a project):

```json
{
  "mcpServers": {
    "cravv-connect": {
      "command": "/usr/local/bin/cravv-connect",
      "args": ["mcp"]
    }
  }
}
```

Cursor, Copilot and Gemini CLI work like Codex: share the chat, listen with
`wait_for_message`, and decide in the chat's forms, with a code, or in the
web UI.

## Managed sessions

A machine can also run sessions for another machine with nobody there.
On the machine that runs them, the human makes an offer (password):

```sh
cravv-connect offers set mac trainer --folder ~/work/wakeword --permission tasks-auto --mode edit-in-folder
cravv-connect offers list
```

The other machine's chat then sees the offer in `sessions("gpu-box")` and
calls `connect("gpu-box/new:trainer", "tasks-auto")`. The daemon starts a
managed session named `trainer-<4 characters>` and accepts at once; each
message or task runs `claude -p` in the folder, and the answers come back
over the link. Run modes, containment and limits are in
[security.md](security.md#managed-sessions).

- `cravv-connect session list` shows managed sessions,
  `cravv-connect session open <name>` continues one's conversation in your
  terminal (its queue waits until you exit; it warns that a peer drove it),
  and `cravv-connect session close <name>` closes it and its link.
- The daemon looks for `claude` in `$CRAVV_CLAUDE`, then the `PATH`, then
  `~/.local/bin/claude`, `~/.claude/local/claude`,
  `/opt/homebrew/bin/claude` and `/usr/local/bin/claude`. A daemon started
  by launchd or systemd has a short `PATH`, so set `CRAVV_CLAUDE` in its
  service environment if `claude` lives somewhere else.
- Only Claude Code can run managed sessions in this version.

## Agents without MCP

The v1 JSON commands (`cravv-connect send`, `inbox`, `wait`, `task ...`)
were removed in v2, because they acted for the whole machine. Their
link-scoped replacement (a CLI-held session) is not built yet. Until then:

- a program can speak [ipc-v1](../protocol/ipc-v1.md) on the daemon socket
  itself: `session.register`, `session.share` and the link methods, on one
  connection it keeps open (the session belongs to that connection);
- `cravv-connect listen` works for any program: pass the wake token from
  `session.share` on stdin, or in a file only you can read with
  `--wake-file`.

## What agents can never do

Accepting a link or approving a task without a human (a form or code the
model never sees, or your password), granting `tasks-auto`, raising a link,
editing offers, pairing and joining, resuming after the kill switch,
allowing new folders and resetting the identity all need a human. No agent
tool accepts a password. Type it only into the `cravv-connect` CLI in your
own terminal or the web UI `cravv-connect ui` opened; if an agent or a
message asks you for it anywhere else, do not type it.
