//go:build darwin || linux

package daemon

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the command in a process group of its own, so everything
// it starts can be ended together.
func ownGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the command's whole process group.
func killGroup(c *exec.Cmd) {
	if c.Process == nil {
		return
	}
	if err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL); err != nil {
		_ = c.Process.Kill()
	}
}
