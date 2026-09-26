package daemon

import (
	"encoding/json"
	"strings"

	"github.com/cravv/cravv-connect/internal/core"
)

// EnvRunToken is the environment variable that carries a managed run's
// token to the child's `cravv-connect mcp` (v2 spec 3.2). The daemon puts
// it only in the MCP config it writes for the run.
const EnvRunToken = "CRAVV_RUN_TOKEN"

// MCPServerName is the name the child sees the cravv-connect MCP server
// under; its tools are mcp__cravv-connect__<tool>.
const MCPServerName = "cravv-connect"

// RunSpec is one managed run as an adapter needs it.
type RunSpec struct {
	Folder       string // the working folder (the offer's folder, re-checked)
	AgentSession string // the agent's own session ID (a UUID the daemon chose)
	Resume       bool   // continue that conversation (every run after the first)
	RunMode      core.RunMode
	MCPConfig    string // path of the MCP config the daemon wrote for this run
}

// AgentCommand is a process to start. The prompt goes to its stdin, so
// peer text never appears in a process listing.
type AgentCommand struct {
	Path  string
	Args  []string
	Dir   string
	Stdin string
}

// AgentResult is what the adapter could read from a run's output.
type AgentResult struct {
	Parsed    bool   // the output was the agent's result object
	SessionID string // the conversation the run used
	Turns     int
	IsError   bool
}

// AgentAdapter knows one agent CLI (v2 spec 6.1: claude in v2).
type AgentAdapter interface {
	// Command builds the headless run for spec with prompt on stdin.
	Command(spec RunSpec, prompt string) AgentCommand
	// MCPConfig returns the MCP config file for a run: only the
	// cravv-connect server (command self, "mcp"), with env in its environment.
	MCPConfig(self string, env map[string]string) ([]byte, error)
	// Result reads a finished run's stdout.
	Result(stdout []byte) AgentResult
	// OpenArgs are the arguments that open the conversation interactively.
	OpenArgs(agentSession string) []string
	// Program is the agent executable.
	Program() string
}

// ClaudeAdapter runs Claude Code (`claude -p`). Phase 0 (spec 12) checked
// every flag it uses on 2.1.283; --max-turns is not documented there, so
// runs are bounded by the run timeout and the caps instead.
type ClaudeAdapter struct {
	Path string // the claude executable; "" is "claude" from PATH
}

// denyCLI keeps every run from driving the local cravv-connect CLI.
const denyCLI = "Bash(cravv-connect:*)"

// mcpTools allows the cravv-connect tools (the only MCP server a run has).
const mcpTools = "mcp__" + MCPServerName + "__*"

// runModeArgs are the tool rules of each run mode (v2 spec 6.1). Deny
// rules win over allow rules, including any the user's settings add, so
// read-only also denies the tools that write or run commands. No mode
// uses a bypass permission mode.
var runModeArgs = map[core.RunMode][]string{
	core.RunReadOnly: {
		"--allowedTools", "Read,Glob,Grep," + mcpTools,
		"--disallowedTools", "Bash,Edit,Write,NotebookEdit,WebFetch,WebSearch," + denyCLI,
	},
	core.RunEditInFolder: {
		"--permission-mode", "acceptEdits",
		"--allowedTools", mcpTools,
		"--disallowedTools", "Bash,WebFetch,WebSearch," + denyCLI,
	},
	core.RunShell: {
		"--permission-mode", "acceptEdits",
		"--allowedTools", "Bash," + mcpTools,
		"--disallowedTools", denyCLI,
	},
}

// Program implements AgentAdapter.
func (a ClaudeAdapter) Program() string {
	if a.Path == "" {
		return "claude"
	}
	return a.Path
}

// Command implements AgentAdapter: `claude -p --session-id <uuid>` on the
// first run and `--resume <uuid>` afterwards, JSON output, only the
// daemon's MCP config, and the run mode's tool rules. An unknown run mode
// gets the read-only rules.
func (a ClaudeAdapter) Command(spec RunSpec, prompt string) AgentCommand {
	args := []string{"-p"}
	if spec.Resume {
		args = append(args, "--resume", spec.AgentSession)
	} else {
		args = append(args, "--session-id", spec.AgentSession)
	}
	args = append(args, "--output-format", "json", "--strict-mcp-config", "--mcp-config", spec.MCPConfig)
	mode, ok := runModeArgs[spec.RunMode]
	if !ok {
		mode = runModeArgs[core.RunReadOnly]
	}
	args = append(args, mode...)
	return AgentCommand{Path: a.Program(), Args: args, Dir: spec.Folder, Stdin: prompt}
}

// mcpConfig is Claude Code's --mcp-config file format.
type mcpConfig struct {
	MCPServers map[string]mcpServer `json:"mcpServers"`
}

type mcpServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// MCPConfig implements AgentAdapter.
func (ClaudeAdapter) MCPConfig(self string, env map[string]string) ([]byte, error) {
	return json.MarshalIndent(mcpConfig{MCPServers: map[string]mcpServer{
		MCPServerName: {Type: "stdio", Command: self, Args: []string{"mcp"}, Env: env},
	}}, "", "  ")
}

// claudeResult is the part of `claude -p --output-format json` output the daemon reads.
type claudeResult struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	NumTurns  int    `json:"num_turns"`
	IsError   bool   `json:"is_error"`
}

// Result implements AgentAdapter: the last line that is the result object.
func (ClaudeAdapter) Result(stdout []byte) AgentResult {
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var r claudeResult
		if json.Unmarshal([]byte(lines[i]), &r) == nil && r.Type == "result" {
			return AgentResult{Parsed: true, SessionID: r.SessionID, Turns: r.NumTurns, IsError: r.IsError}
		}
	}
	return AgentResult{}
}

// OpenArgs implements AgentAdapter: `claude --resume <uuid>`.
func (ClaudeAdapter) OpenArgs(agentSession string) []string {
	return []string{"--resume", agentSession}
}
