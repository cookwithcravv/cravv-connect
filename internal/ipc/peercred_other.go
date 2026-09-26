//go:build !darwin && !linux

package ipc

import (
	"errors"
	"net"
)

// peerUID is not implemented here: cravv-connect supports macOS and Linux
// only, and on other systems every connection is refused.
func peerUID(net.Conn) (int, error) {
	return -1, errors.New("peer credentials are not supported on this OS")
}

func peerPID(net.Conn) (int, error) {
	return -1, errors.New("peer credentials are not supported on this OS")
}
