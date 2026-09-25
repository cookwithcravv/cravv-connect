// Command cravv-connect is the single binary: daemon, mcp, hook and CLI.
package main

import (
	"os"

	"github.com/cravv/cravv-connect/internal/cli"
	"github.com/cravv/cravv-connect/internal/config"
	"github.com/cravv/cravv-connect/internal/install"
)

func main() {
	env := cli.DefaultEnv()
	if home, err := os.UserHomeDir(); err == nil {
		run := install.ExecRunner{}
		env.Agents = install.DefaultRegistry(home, run)
		if paths, err := config.ResolvePaths(); err == nil {
			svc, err := install.NewService(install.ServiceConfig{Home: home, CravvHome: paths.Home, LogPath: paths.Log, StderrPath: paths.StderrLog()}, run)
			if err == nil {
				env.Service, env.ServiceSetup = svc, svc
			}
		}
	}
	os.Exit(cli.Main(os.Args[1:], env))
}
