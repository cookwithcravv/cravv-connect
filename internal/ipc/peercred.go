package ipc

import (
	"errors"
	"fmt"
	"net"
	"os"
)

// errNotUnix is peerPID's answer for a connection that is not a unix
// socket (an in-process pipe): it has no peer process.
var errNotUnix = errors.New("not a unix socket")

// selfUID is the daemon's UID; a variable so tests can pretend to be someone else.
var selfUID = os.Getuid

// checkPeerUID accepts a connection only from the daemon's own user. The
// socket file is 0600 in a 0700 directory already; this also covers a
// descriptor passed to, or a directory reachable by, another user.
func checkPeerUID(peer int, err error, self int) error {
	if err != nil {
		return fmt.Errorf("ipc: read peer credentials: %w", err)
	}
	if peer != self {
		return fmt.Errorf("ipc: connection from uid %d refused (daemon runs as uid %d)", peer, self)
	}
	return nil
}

// verifyPeer checks a newly accepted connection. Connections that are not
// unix sockets (tests serving net.Pipe through ServeConn) never reach it.
func verifyPeer(c net.Conn) error {
	uid, err := peerUID(c)
	return checkPeerUID(uid, err, selfUID())
}
