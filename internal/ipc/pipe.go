package ipc

import (
	"context"
	"net"
)

// NewClient wraps an open connection to the daemon, such as one end of
// net.Pipe. The client owns conn and closes it on Close.
func NewClient(conn net.Conn) *Client {
	c := &Client{conn: conn, pending: map[uint64]chan Response{}, done: make(chan struct{})}
	go c.readLoop()
	return c
}

// Pipe opens an in-process connection to s. The server end is served by
// ServeConn exactly like a socket connection: it has its own ConnState, so
// the same gates apply and its password unlock window is its own. Only the
// peer-credential check is skipped, because the caller is this process.
// done is closed once ServeConn has returned, after the client closed the
// connection or ctx ended.
func (s *Server) Pipe(ctx context.Context) (c *Client, done <-chan struct{}) {
	serverEnd, clientEnd := net.Pipe()
	d := make(chan struct{})
	go func() {
		defer close(d)
		s.ServeConn(ctx, serverEnd)
	}()
	return NewClient(clientEnd), d
}
