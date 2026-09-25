// Command cravv-connect is the single binary: daemon, mcp, hook and CLI.
package main

import (
	"os"

	"github.com/cravv/cravv-connect/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.DefaultEnv()))
}
