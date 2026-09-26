package daemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestClaudeCommandPerRunMode(t *testing.T) {
	const uuid = "0b5c2f6e-8a1d-4c3e-9f70-2d6a1b3c4d5e"
	base := func(resume bool) []string {
		flag := "--session-id"
		if resume {
			flag = "--resume"
		}
		return []string{"-p", flag, uuid, "--output-format", "json", "--strict-mcp-config", "--mcp-config", "/state/runs/R1.json"}
	}
	for _, c := range []struct {
		mode   core.RunMode
		resume bool
		want   []string
	}{
		{core.RunReadOnly, false, append(base(false),
			"--allowedTools", "Read,Glob,Grep,mcp__cravv-connect__*",
			"--disallowedTools", "Bash,Edit,Write,NotebookEdit,WebFetch,WebSearch,Bash(cravv-connect:*)")},
		{core.RunEditInFolder, true, append(base(true),
			"--permission-mode", "acceptEdits",
			"--allowedTools", "mcp__cravv-connect__*",
			"--disallowedTools", "Bash,WebFetch,WebSearch,Bash(cravv-connect:*)")},
		{core.RunShell, true, append(base(true),
			"--permission-mode", "acceptEdits",
			"--allowedTools", "Bash,mcp__cravv-connect__*",
			"--disallowedTools", "Bash(cravv-connect:*)")},
		{"bogus", false, append(base(false),
			"--allowedTools", "Read,Glob,Grep,mcp__cravv-connect__*",
			"--disallowedTools", "Bash,Edit,Write,NotebookEdit,WebFetch,WebSearch,Bash(cravv-connect:*)")},
	} {
		spec := RunSpec{Folder: "/srv/proj", AgentSession: uuid, Resume: c.resume, RunMode: c.mode, MCPConfig: "/state/runs/R1.json"}
		cmd := ClaudeAdapter{}.Command(spec, "the prompt")
		if cmd.Path != "claude" || cmd.Dir != "/srv/proj" || cmd.Stdin != "the prompt" {
			t.Fatalf("%s: command %+v", c.mode, cmd)
		}
		if !reflect.DeepEqual(cmd.Args, c.want) {
			t.Errorf("%s resume=%v:\n got %q\nwant %q", c.mode, c.resume, cmd.Args, c.want)
		}
		joined := strings.Join(cmd.Args, " ")
		for _, never := range []string{"bypassPermissions", "dangerously", "the prompt"} {
			if strings.Contains(joined, never) {
				t.Errorf("%s: %q in the arguments", c.mode, never)
			}
		}
	}
	if got := (ClaudeAdapter{Path: "/opt/claude"}).Command(RunSpec{}, "").Path; got != "/opt/claude" {
		t.Fatalf("path %q", got)
	}
	if got := (ClaudeAdapter{}).OpenArgs(uuid); !reflect.DeepEqual(got, []string{"--resume", uuid}) {
		t.Fatalf("OpenArgs %q", got)
	}
}

func TestClaudeMCPConfigHoldsOnlyCravvConnect(t *testing.T) {
	b, err := ClaudeAdapter{}.MCPConfig("/usr/local/bin/cravv-connect", map[string]string{EnvRunToken: "tok", "CRAVV_HOME": "/h"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]map[string]map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	servers := got["mcpServers"]
	if len(got) != 1 || len(servers) != 1 {
		t.Fatalf("config %s", b)
	}
	s := servers["cravv-connect"]
	if s["type"] != "stdio" || s["command"] != "/usr/local/bin/cravv-connect" || !reflect.DeepEqual(s["args"], []any{"mcp"}) ||
		!reflect.DeepEqual(s["env"], map[string]any{"CRAVV_RUN_TOKEN": "tok", "CRAVV_HOME": "/h"}) {
		t.Fatalf("server %v", s)
	}
}

func TestClaudeResult(t *testing.T) {
	out := []byte(`{"type":"result","subtype":"success","is_error":false,"num_turns":3,"result":"done","session_id":"0b5c2f6e-8a1d-4c3e-9f70-2d6a1b3c4d5e"}` + "\n")
	if r := (ClaudeAdapter{}).Result(out); r != (AgentResult{Parsed: true, SessionID: "0b5c2f6e-8a1d-4c3e-9f70-2d6a1b3c4d5e", Turns: 3}) {
		t.Fatalf("Result = %+v", r)
	}
	if r := (ClaudeAdapter{}).Result([]byte("warning\n" + `{"type":"result","is_error":true,"num_turns":1}`)); !r.Parsed || !r.IsError {
		t.Fatalf("error result = %+v", r)
	}
	for _, bad := range []string{"", "not json", `{"type":"assistant"}`} {
		if r := (ClaudeAdapter{}).Result([]byte(bad)); r.Parsed {
			t.Fatalf("%q parsed as %+v", bad, r)
		}
	}
}
