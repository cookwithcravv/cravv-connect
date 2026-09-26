package mcpserver

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type killSwitchTool struct{}

func (killSwitchTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "kill_switch", "Emergency stop: close every link, disconnect from every machine and refuse all traffic until the human resumes with their password.", annCutOff,
		func(ctx context.Context, _ noArgs) (string, error) {
			if err := c.Call(ctx, ipc.MethodKill, nil, nil); err != nil {
				return "", err
			}
			return "Kill switch is on. Only the human can resume (cravv-connect resume).", nil
		})
}
