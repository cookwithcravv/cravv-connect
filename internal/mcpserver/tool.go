package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolRegistrar adds one tool to the server. New tools are new registrars in
// the list returned by Tools; nothing else changes.
type ToolRegistrar interface {
	Register(s *mcp.Server, c Caller)
}

// Tools returns every tool (v2 spec 7.3), in the order clients list them.
func Tools() []ToolRegistrar {
	return []ToolRegistrar{
		sessionShareTool{}, sessionCloseTool{}, sessionSetTool{},
		machinesTool{}, sessionsTool{}, connectTool{}, linksTool{}, disconnectTool{}, restrictTool{},
		checkInboxTool{}, waitForMessageTool{},
		sendMessageTool{},
		createTaskTool{}, getTaskTool{}, claimTaskTool{}, updateTaskTool{},
		completeTaskTool{}, failTaskTool{}, cancelTaskTool{},
		sendFileTool{},
		killSwitchTool{},
	}
}

// annotations builds tool hints. MCP defaults destructiveHint and
// openWorldHint to true, so both are always set.
func annotations(readOnly, destructive, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, OpenWorldHint: &openWorld}
}

// Tool hints by kind: reads, reads that ask another machine, changes on
// this machine, sends that reach another machine's agent, and cut-offs.
var (
	annRead       = annotations(true, false, false)
	annReadRemote = annotations(true, false, true)
	annLocal      = annotations(false, false, false)
	annSend       = annotations(false, false, true)
	annCutOff     = annotations(false, true, false)
)

// addTool registers a typed tool whose handler returns plain text. Handler
// errors become tool errors (IsError) so the model can see and react to them.
func addTool[In any](s *mcp.Server, name, description string, ann *mcp.ToolAnnotations, fn func(ctx context.Context, in In) (string, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: description, Annotations: ann},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			text, err := fn(ctx, in)
			if err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
		})
}

// jsonText renders a structured result for the model.
func jsonText(v any) (string, error) {
	// No HTML escaping: wrapped fields must read as <remote_message>, not
	// \u003cremote_message\u003e. Peer text inside them is already escaped.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// callJSON calls method and returns its result as JSON text.
func callJSON[R any](ctx context.Context, c Caller, method string, params any) (string, error) {
	var r R
	if err := c.Call(ctx, method, params, &r); err != nil {
		return "", err
	}
	return jsonText(r)
}

// noArgs is the input of tools without parameters.
type noArgs struct{}
