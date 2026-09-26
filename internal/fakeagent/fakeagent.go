// Package fakeagent stands in for the claude CLI in tests. A test binary
// whose TestMain calls Main becomes the fake agent when CRAVV_FAKE_AGENT
// is set, so the SessionHost starts real processes (process groups,
// timeouts, the MCP config, the run token) without calling Claude.
package fakeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Environment the fake agent reads (the daemon passes its own environment
// to the child, so a test sets these with t.Setenv).
const (
	// EnvMode selects the behaviour: ok, fail or hang.
	EnvMode = "CRAVV_FAKE_AGENT"
	// EnvLog is a file every run appends its Record to, one JSON line.
	EnvLog = "CRAVV_FAKE_AGENT_LOG"
)

// Record is one run as the fake agent saw it.
type Record struct {
	Mode     string            `json:"mode"`
	Args     []string          `json:"args"`
	Dir      string            `json:"dir"`
	Prompt   string            `json:"prompt"`
	PID      int               `json:"pid"`
	ChildPID int               `json:"child_pid,omitempty"`
	Token    string            `json:"token,omitempty"`
	Env      map[string]string `json:"env,omitempty"`     // the MCP server's environment from the config
	HasToken bool              `json:"has_token_env"`     // CRAVV_RUN_TOKEN in the agent's own environment
	Results  map[string]string `json:"results,omitempty"` // what the run's daemon calls returned
}

// Main runs the fake agent and exits when EnvMode is set; otherwise it
// returns at once.
func Main() {
	mode := os.Getenv(EnvMode)
	if mode == "" {
		return
	}
	os.Exit(run(mode))
}

// actions are the modes that talk to the daemon (added by later code).
var actions = map[string]func(rec *Record){}

func run(mode string) int {
	prompt, _ := io.ReadAll(os.Stdin)
	dir, _ := os.Getwd()
	rec := Record{Mode: mode, Args: os.Args[1:], Dir: dir, Prompt: string(prompt), PID: os.Getpid(), HasToken: os.Getenv("CRAVV_RUN_TOKEN") != ""}
	session := argAfter(os.Args, "--session-id")
	if session == "" {
		session = argAfter(os.Args, "--resume")
	}
	if cfg := argAfter(os.Args, "--mcp-config"); cfg != "" {
		rec.Env = mcpEnv(cfg)
		rec.Token = rec.Env["CRAVV_RUN_TOKEN"]
	}
	code := 0
	switch mode {
	case "ok":
	case "fail":
		fmt.Fprintln(os.Stderr, "fake agent: failing on purpose")
		code = 3
	case "hang":
		// A grandchild in the same process group: a timeout must end it too.
		child := exec.Command("sleep", "300")
		if err := child.Start(); err == nil {
			rec.ChildPID = child.Process.Pid
		}
		write(rec)
		time.Sleep(300 * time.Second)
		return 0
	default:
		act, ok := actions[mode]
		if !ok {
			code = 2
			break
		}
		act(&rec)
	}
	write(rec)
	if code == 0 {
		out, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 2, "result": "done", "session_id": session})
		fmt.Println(string(out))
	}
	return code
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// mcpEnv reads the cravv-connect server's environment from an MCP config.
func mcpEnv(path string) map[string]string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg struct {
		MCPServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return nil
	}
	return cfg.MCPServers["cravv-connect"].Env
}

func write(rec Record) {
	path := os.Getenv(EnvLog)
	if path == "" {
		return
	}
	b, _ := json.Marshal(rec)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

// Records reads every Record in a log file (none when it does not exist).
func Records(path string) ([]Record, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	dec := json.NewDecoder(bytes.NewReader(b))
	for dec.More() {
		var r Record
		if err := dec.Decode(&r); err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}
