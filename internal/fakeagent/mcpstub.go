package fakeagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// EnvMCPLog makes a test binary a stub MCP server (stdio, one tool named
// ping) that appends "called ping" to this file on every call. The opt-in
// real-claude containment test puts it in the run's MCP config to see that
// the cravv-connect tools still work under the containment flags.
const EnvMCPLog = "CRAVV_FAKE_MCP_LOG"

// serveMCP answers MCP requests on stdin until it closes.
func serveMCP(log string) int {
	record(log, "started")
	out := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion string `json:"protocolVersion"`
				Name            string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || len(m.ID) == 0 {
			continue // a notification
		}
		var result any = map[string]any{}
		switch m.Method {
		case "initialize":
			v := m.Params.ProtocolVersion
			if v == "" {
				v = "2024-11-05"
			}
			result = map[string]any{"protocolVersion": v, "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "cravv-connect-stub", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "ping", "description": "Records a ping and returns PONG.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			}}}
		case "tools/call":
			record(log, "called "+m.Params.Name)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "PONG"}}}
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
	}
	return 0
}

func record(path, line string) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}
