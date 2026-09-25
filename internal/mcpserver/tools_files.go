package mcpserver

import (
	"context"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sendFileTool struct{}

type sendFileIn struct {
	To   string `json:"to" jsonschema:"peer alias"`
	Path string `json:"path" jsonschema:"file inside this project (or a folder the human allowed), up to 100 MB"`
}

func (sendFileTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "send_file", "Send a file to a paired machine. Only regular files inside this project or folders the human allowed; dotfiles, dot-directories and secret files such as keys, certificates and .env files are refused (.env*, id_*, credentials*.json, service-account*.json, *.pem, *.key, *.env, *.p12, *.pfx, *.jks, *.keystore, *.kdbx, *.ppk, *.ovpn).",
		func(ctx context.Context, in sendFileIn) (string, error) {
			return callJSON[ipc.FileSendResult](ctx, c, ipc.MethodFileSend, ipc.FileSendParams{To: in.To, Path: in.Path})
		})
}
