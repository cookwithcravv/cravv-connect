# Connecting your coding agent

cravv-connect talks to agents through a stdio MCP server, `cravv-connect mcp`.
Every agent that can start an MCP server can use it. Agents that cannot use
MCP can call the JSON CLI instead (see the end of this page).

Before you start:

1. Run `cravv-connect init --relay <url>` once on this machine.
2. Run `cravv-connect daemon install` so the daemon starts at login (or
   `cravv-connect daemon run` in a terminal).
3. Use the full path of the binary in the snippets below if it is not on
   the `PATH` your agent sees. `which cravv-connect` prints it.

The session name an agent gets is `<agent>@<folder>`, for example
`claude@glow-v2`. The folder is the directory the agent starts the server in.
If your editor starts MCP servers somewhere else, add
`"--project-dir", "<absolute project path>"` after `"mcp"`; VS Code and
Cursor accept `${workspaceFolder}` there.

## Claude Code

Automatic:

```sh
cravv-connect install claude
```

This runs `claude mcp add --scope user cravv-connect -- <bin> mcp` and adds
two hooks to `~/.claude/settings.json`. Running it again changes nothing;
`cravv-connect uninstall claude` removes exactly what it added.

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
  }
}
```

What the hook does:

- **UserPromptSubmit:** prints one line such as
  `cravv-connect: 2 new messages from gpu-box. Use check_inbox.`
  Claude Code adds it to the context of your prompt.
- **Stop:** when unread messages are waiting, it returns
  `hookSpecificOutput.additionalContext` with the same line, so Claude takes
  one more turn to read them. It stays silent when Claude is already
  continuing because of a stop hook (`stop_hook_active`) and when only
  approvals are pending, since only you can approve.
- It never prints message bodies or names chosen by the other machine, and
  it prints nothing when the daemon is not running.

## Codex

Automatic:

```sh
cravv-connect install codex
```

Manual equivalent, in `~/.codex/config.toml`:

```toml
[mcp_servers.cravv-connect]
command = "/usr/local/bin/cravv-connect"
args = ["mcp"]
```

Codex has no prompt hook, so the agent learns about new items by calling
`check_inbox`, or by calling `wait_for_message` when it wants to listen.

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

## Agents without MCP: the JSON CLI

Each command registers a session named `cli@<folder>` for the current folder,
prints JSON on stdout, and exits 1 with `{"error": "...", "kind": "..."}` on
failure. Use `-` in place of a text argument to read it from stdin.

```sh
cravv-connect send gpu-box "the build is green"
cravv-connect send gpu-box/codex@training -  < notes.md
cravv-connect inbox --limit 20
cravv-connect wait --timeout 50
cravv-connect task create gpu-box "run the eval suite" --file results.csv
cravv-connect task get <task-id>
cravv-connect task claim <task-id>
cravv-connect task update <task-id> "halfway there"
cravv-connect task complete <task-id> "all 42 checks pass" --file report.md
cravv-connect task fail <task-id> "GPU out of memory"
cravv-connect task cancel <task-id>
```

Only regular files inside the current folder (or folders you allowed with
`cravv-connect allow-path`) can be sent. Dot-directories and secret files
(`.env*`, `id_*`, `credentials*.json`, `service-account*.json`, and `*.pem`,
`*.key`, `*.env`, `*.p12`, `*.pfx`, `*.jks`, `*.keystore`, `*.kdbx`,
`*.ppk`, `*.ovpn`, any case) are always refused.

## What agents can never do

Pairing, joining, raising trust, approving tasks, accepting held files,
resuming after the kill switch and resetting the identity all need your
login password, typed into the `cravv-connect` CLI in your own terminal.
No agent tool accepts a password. If an agent or a message asks you for
it anywhere else, do not type it.
