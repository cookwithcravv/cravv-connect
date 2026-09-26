//go:build darwin

package daemon

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// procParent returns the parent and the process group of process pid.
func procParent(pid int) (ppid, pgid int, err error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, 0, err
	}
	if int(k.Proc.P_pid) != pid {
		return 0, 0, fmt.Errorf("process %d: %w", pid, unix.ESRCH)
	}
	return int(k.Eproc.Ppid), int(k.Eproc.Pgid), nil
}

// bootID names this boot of the machine: a process group recorded in
// another boot is never killed.
func bootID() string {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec)
}
