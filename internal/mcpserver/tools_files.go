package mcpserver

import (
	"context"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sendFileTool struct{}

type sendFileIn struct {
	Link int64  `json:"link" jsonschema:"link number (see the link attribute on received items)"`
	Path string `json:"path" jsonschema:"file inside this project (or a folder the human allowed), up to 100 MB"`
}

func (sendFileTool) Register(s *mcp.Server, c Caller) {
	addTool(s, "send_file", "Send a file on a link. Only regular files inside this project or folders the human allowed; dotfiles, dot-directories and secret files such as keys, certificates and .env files are refused (.env*, id_*, credentials*.json, service-account*.json, *.pem, *.key, *.env, *.p12, *.pfx, *.jks, *.keystore, *.kdbx, *.ppk, *.ovpn).", annSend,
		func(ctx context.Context, in sendFileIn) (string, error) {
			return callJSON[ipc.FileSendResult](ctx, c, ipc.MethodFileSend, ipc.FileSendParams{Link: in.Link, Path: in.Path})
		})
}
