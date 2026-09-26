package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

func init() { Register(newVersionCmd) }

// BuildVersion is Version as stamped by the release build, or the module
// version for `go install ...@vX.Y.Z`, or "dev".
func BuildVersion() string {
	if Version != "dev" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return Version
}

func newVersionCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version of this binary",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintf(env.Stdout, "cravv-connect %s (%s/%s)\n", BuildVersion(), runtime.GOOS, runtime.GOARCH)
			return nil
		},
	}
}
