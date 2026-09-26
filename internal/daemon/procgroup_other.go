//go:build !(darwin || linux)

package daemon

import "os/exec"

// ownGroup is a no-op where process groups are not available.
func ownGroup(*exec.Cmd) {}

// killGroup kills the command only.
func killGroup(c *exec.Cmd) {
	if c.Process != nil {
		_ = c.Process.Kill()
	}
}
