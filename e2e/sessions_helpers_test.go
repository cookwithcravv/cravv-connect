package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// SharedChat is one agent chat that shared a session: its connection (the
// session is bound to it) and the share result with the two tokens.
type SharedChat struct {
	C    *ipc.Client
	Name string
	Res  ipc.ShareResult
}

// Share opens a connection, registers it as agent in the node's project
// folder and shares a session called name with the given visibility.
func (n *Node) Share(agent, name, visibility string) *SharedChat {
	n.t.Helper()
	c, _ := n.Session(agent)
	var res ipc.ShareResult
	Call(n.t, c, ipc.MethodSessionShare, ipc.SessionShareParams{Name: name, Purpose: name + " work", Visibility: visibility}, &res)
	return &SharedChat{C: c, Name: name, Res: res}
}

// Reattach opens a new connection for agent and takes the session over
// with its reattach token.
func (n *Node) Reattach(agent string, s *SharedChat) *SharedChat {
	n.t.Helper()
	c, _ := n.Session(agent)
	var v ipc.SharedSessionView
	Call(n.t, c, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: s.Res.ReattachToken}, &v)
	return &SharedChat{C: c, Name: s.Name, Res: s.Res}
}

// AllLinks lists every link on the node (the human's view).
func (n *Node) AllLinks() []ipc.LinkView {
	n.t.Helper()
	var r ipc.LinksResult
	n.oneCall(ipc.MethodLinks, nil, &r)
	return r.Links
}

// Link returns the node's link number num.
func (n *Node) Link(num int64) ipc.LinkView {
	n.t.Helper()
	for _, l := range n.AllLinks() {
		if l.Link == num {
			return l
		}
	}
	n.t.Fatalf("%s has no link %d", n.Name, num)
	return ipc.LinkView{}
}

// WaitLink polls the node's links until one matches.
func (n *Node) WaitLink(timeout time.Duration, what string, match func(ipc.LinkView) bool) ipc.LinkView {
	n.t.Helper()
	var found ipc.LinkView
	Eventually(n.t, timeout, n.Name+": "+what, func() bool {
		for _, l := range n.AllLinks() {
			if match(l) {
				found = l
				return true
			}
		}
		return false
	})
	return found
}

// Decide accepts (with the password) or rejects the node's pending link num.
func (n *Node) Decide(num int64, accept bool, permission string) ipc.LinkView {
	n.t.Helper()
	var v ipc.LinkView
	Call(n.t, n.Unlocked(), ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: num, Accept: accept, Permission: permission}, &v)
	return v
}

// Connect asks target for a link from the shared chat s.
func Connect(t *testing.T, s *SharedChat, target, permission, note string) ipc.LinkView {
	t.Helper()
	var v ipc.LinkView
	Call(t, s.C, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: target, Permission: permission, Note: note}, &v)
	return v
}

// Linked is an active link between two shared chats, with each side's number.
type Linked struct {
	A, B       *SharedChat
	ANum, BNum int64
}

// LinkUp shares "lead" on a (private) and "trainer" on b (all peers), links
// them and has b's human accept at permission with the password.
func LinkUp(t *testing.T, a, b *Node, permission string) Linked {
	t.Helper()
	sa := a.Share("claude", "lead", "private")
	sb := b.Share("claude", "trainer", "all-peers")
	return LinkChats(t, a, b, sa, sb, permission)
}

// LinkChats links two chats that already shared.
func LinkChats(t *testing.T, a, b *Node, sa, sb *SharedChat, permission string) Linked {
	t.Helper()
	out := Connect(t, sa, b.Name+"/"+sb.Name, permission, "")
	in := b.WaitLink(wait, "link request from "+sa.Name, func(l ipc.LinkView) bool {
		return l.State == "pending" && l.Direction == "in" && l.RemoteSession == sa.Name && l.Session == sb.Name
	})
	b.Decide(in.Link, true, permission)
	a.WaitLink(wait, "link accepted", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
	return Linked{A: sa, B: sb, ANum: out.Link, BNum: in.Link}
}

// Listen runs session.listen with the wake token on a fresh connection.
func (n *Node) Listen(token string, timeout time.Duration) ipc.ListenResult {
	n.t.Helper()
	c, err := ipc.Dial(n.Paths.Socket)
	if err != nil {
		n.t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout+10*time.Second)
	defer cancel()
	var r ipc.ListenResult
	if err := c.Call(ctx, ipc.MethodSessionListen, ipc.SessionListenParams{WakeToken: token, TimeoutS: int(timeout / time.Second)}, &r); err != nil {
		n.t.Fatalf("session.listen: %v", err)
	}
	return r
}
