//go:build darwin || linux

package daemon

import (
	"errors"
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

// killPGID kills process group pgid (a run's, recorded when it started).
// A missing group is not an error.
func killPGID(pgid int) error {
	if pgid <= 1 {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
