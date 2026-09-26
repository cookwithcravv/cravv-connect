//go:build darwin

package ipc

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the effective UID of the process at the other end of a
// unix socket (LOCAL_PEERCRED).
func peerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	var cred *unix.Xucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	}); err != nil {
		return -1, err
	}
	if serr != nil {
		return -1, serr
	}
	return int(cred.Uid), nil
}

// peerPID returns the PID of the process at the other end of a unix socket
// (LOCAL_PEERPID).
func peerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return -1, errNotUnix
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return -1, err
	}
	var pid int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return -1, err
	}
	if serr != nil {
		return -1, serr
	}
	return pid, nil
}
