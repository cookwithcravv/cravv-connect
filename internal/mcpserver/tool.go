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

// Tools returns every tool, in the order clients list them.
func Tools() []ToolRegistrar {
	return []ToolRegistrar{
		statusTool{},
		sendMessageTool{}, checkInboxTool{}, waitForMessageTool{},
		createTaskTool{}, getTaskTool{}, claimTaskTool{}, updateTaskTool{},
		completeTaskTool{}, failTaskTool{}, cancelTaskTool{},
		sendFileTool{},
		pausePeerTool{}, unpairPeerTool{}, lowerTrustTool{}, killSwitchTool{},
	}
}

// addTool registers a typed tool whose handler returns plain text. Handler
// errors become tool errors (IsError) so the model can see and react to them.
func addTool[In any](s *mcp.Server, name, description string, fn func(ctx context.Context, in In) (string, error)) {
	mcp.AddTool(s, &mcp.Tool{Name: name, Description: description},
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
