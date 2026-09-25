package relayserver

import "sync"

// hub tracks the single live connection per mailbox.
type hub struct {
	mu    sync.Mutex
	conns map[string]*mailboxConn
}

func newHub() *hub { return &hub{conns: map[string]*mailboxConn{}} }

// register makes c the live connection for its mailbox and returns the one it replaced.
func (h *hub) register(c *mailboxConn) *mailboxConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.conns[c.mailbox]
	h.conns[c.mailbox] = c
	return old
}

// unregister removes c if it is still the live connection.
func (h *hub) unregister(c *mailboxConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns[c.mailbox] == c {
		delete(h.conns, c.mailbox)
	}
}

// notify wakes the live connection of mailbox, if any, to push new frames.
func (h *hub) notify(mailbox string) {
	h.mu.Lock()
	c := h.conns[mailbox]
	h.mu.Unlock()
	if c != nil {
		c.wakeUp()
	}
}
