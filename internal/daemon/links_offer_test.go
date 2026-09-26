package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// v2Offers is a node that offers managed sessions: its offer rules, its
// SessionHost, and a project folder.
type v2Offers struct {
	offers *OfferService
	host   *SessionHost
	proj   string
	rules  FolderRules
}

// withOffers gives node v offer rules and a SessionHost, wired the way
// buildManaged wires the daemon.
func withOffers(t *testing.T, v *v2Node) v2Offers {
	t.Helper()
	rules, proj := offerTree(t)
	offers := NewOfferService(v.st, v.peers, rules, v.net.clock, nil)
	host := NewSessionHost(HostDeps{Offers: offers, Store: v.st, Sessions: v.shared, Clock: v.net.clock})
	offers.AddObserver(host)
	v.links.d.Managed = host
	v.links.AddCloseObserver(host)
	v.discover.SetOffers(offers)
	return v2Offers{offers: offers, host: host, proj: proj, rules: rules}
}

// offer sets an offer for machine peer on v (password path).
func (o v2Offers) offer(t *testing.T, peer, label string, perm core.Permission, mutate func(*OfferInput)) store.Offer {
	t.Helper()
	in := OfferInput{Peer: peer, Label: label, Folder: o.proj, Permission: perm}
	if mutate != nil {
		mutate(&in)
	}
	got, err := o.offers.Set(context.Background(), in, AuthPassword)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var managedName = regexp.MustCompile(`^trainer-[a-z0-9]{4}$`)

func TestLinkRequestToAnOfferStartsAManagedSession(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	gpu := withOffers(t, b)
	gpu.offer(t, "alice", "trainer", core.PermTasksAuto, nil)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})

	_, listed, err := a.discover.List(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Offers) != 1 || listed.Offers[0].Label != "trainer" || listed.Offers[0].MaxPermission != core.PermTasksAuto ||
		listed.Offers[0].Agent != "claude" {
		t.Fatalf("alice sees offers %+v", listed.Offers)
	}
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/new:trainer", core.PermTasksAuto, "train it")
	if err != nil {
		t.Fatal(err)
	}
	if out.State != store.LinkPending || out.RemoteName != "new:trainer" || out.RemoteSession != "" {
		t.Fatalf("pending link %+v", out)
	}
	if req := v2Body[core.LinkRequestBody](t, n.sent(core.KindLinkRequest)[0]); req.OfferID != listed.Offers[0].OfferID || req.ToSessionID != "" {
		t.Fatalf("request body %+v", req)
	}
	n.pump()

	in := b.linkOf(t, a, out.ID)
	if in.State != store.LinkActive || in.PermissionIn != core.PermTasksAuto || in.PermissionOut != core.PermMessages {
		t.Fatalf("gpu side %+v (accepted at once, at the offer's level)", in)
	}
	sess, err := b.shared.Get(ctx, in.Session)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Kind != core.SessionManaged || sess.State != core.SessionOpen || !managedName.MatchString(sess.Name) ||
		sess.ProjectDir != gpu.proj || sess.Visibility.Mode != core.VisibilityPrivate {
		t.Fatalf("managed session %+v", sess)
	}
	m, err := b.st.GetManaged(ctx, sess.ID)
	if err != nil || m.LinkID != out.ID || m.Peer != a.id || m.Started ||
		!regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(m.AgentSession) {
		t.Fatalf("managed record %+v, %v", m, err)
	}
	if len(b.notices(t, sess.ID)) != 0 || len(b.desktop.all()) != 0 {
		t.Fatal("nobody is asked about a request an offer covers")
	}
	got := a.linkOf(t, b, out.ID)
	if got.State != store.LinkActive || got.PermissionOut != core.PermTasksAuto || got.RemoteSession != sess.ID || got.RemoteName != sess.Name {
		t.Fatalf("alice after accept %+v", got)
	}

	// The grant is the lower of the proposal and the offer.
	out2, err := a.links.Connect(ctx, lead.Session.ID, "bob/new:trainer", core.PermMessages, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	if l := b.linkOf(t, a, out2.ID); l.PermissionIn != core.PermMessages {
		t.Fatalf("proposed messages, granted %s", l.PermissionIn)
	}
	gpu.offer(t, "alice", "reader", core.PermMessages, nil)
	out3, err := a.links.Connect(ctx, lead.Session.ID, "bob/new:reader", core.PermTasksAuto, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	if l := a.linkOf(t, b, out3.ID); l.State != store.LinkActive || l.PermissionOut != core.PermMessages {
		t.Fatalf("a messages offer grants messages: %+v", l)
	}
	if _, err := a.links.Connect(ctx, lead.Session.ID, "bob/new:nope", core.PermMessages, ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown offer: %v", err)
	}
}

// A managed session has no human to ask, so a tasks-ask proposal is
// granted as messages, never tasks-ask.
func TestOfferNeverGrantsTasksAsk(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	gpu := withOffers(t, b)
	gpu.offer(t, "alice", "trainer", core.PermTasksAuto, nil)
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	out, err := a.links.Connect(ctx, lead.Session.ID, "bob/new:trainer", core.PermTasksAsk, "")
	if err != nil {
		t.Fatal(err)
	}
	n.pump()
	if l := b.linkOf(t, a, out.ID); l.State != store.LinkActive || l.PermissionIn != core.PermMessages {
		t.Fatalf("proposed tasks-ask, the gpu side granted %+v", l)
	}
	if l := a.linkOf(t, b, out.ID); l.PermissionOut != core.PermMessages {
		t.Fatalf("proposed tasks-ask, alice was told %s", l.PermissionOut)
	}
}

func TestOfferRequestLimits(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	gpu := withOffers(t, b)
	o := gpu.offer(t, "alice", "trainer", core.PermTasksAuto, func(in *OfferInput) { in.MaxConcurrent = 1; in.RunsPerDay = 3 })
	lead := shareOn(t, a, 1, "lead", core.Visibility{})
	connect := func() store.Link {
		t.Helper()
		out, err := a.links.Connect(ctx, lead.Session.ID, "bob/new:trainer", core.PermTasksAuto, "")
		if err != nil {
			t.Fatal(err)
		}
		n.pump()
		return a.linkOf(t, b, out.ID)
	}
	first := connect()
	if first.State != store.LinkActive {
		t.Fatalf("first %+v", first)
	}
	if second := connect(); second.State != store.LinkClosed || second.Reason != core.RejectBusy {
		t.Fatalf("over max_concurrent: %+v", second)
	}
	// Closing the link closes the managed session, which frees the slot.
	if err := a.links.Disconnect(ctx, "", first.Num); err != nil {
		t.Fatal(err)
	}
	n.pump()
	gpuSide := b.linkOf(t, a, first.ID)
	if s, _ := b.shared.Get(ctx, gpuSide.Session); s.State != core.SessionClosed {
		t.Fatalf("the managed session must close with its link: %+v", s)
	}
	third := connect()
	if third.State != store.LinkActive {
		t.Fatalf("after the first closed: %+v", third)
	}
	for i := range 3 {
		if err := b.st.AddRun(ctx, store.ManagedRun{ID: core.NewID(), SessionID: "S", Peer: a.id, LinkID: "L", StartedAt: d2Epoch.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.links.Disconnect(ctx, "", b.linkOf(t, a, third.ID).Num); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if day := connect(); day.State != store.LinkClosed || day.Reason != core.RejectBusy {
		t.Fatalf("over runs_per_day: %+v", day)
	}
	n.clock.Advance(25 * time.Hour)
	// A folder that moved refuses the request (policy).
	if err := os.Rename(gpu.proj, gpu.proj+"-moved"); err != nil {
		t.Fatal(err)
	}
	if moved := connect(); moved.State != store.LinkClosed || moved.Reason != core.RejectPolicy {
		t.Fatalf("folder gone: %+v", moved)
	}
	if err := os.Rename(gpu.proj+"-moved", gpu.proj); err != nil {
		t.Fatal(err)
	}
	if ok := connect(); ok.State != store.LinkActive {
		t.Fatalf("folder back: %+v", ok)
	}
	// Removing the offer closes its sessions (and their links).
	if _, err := gpu.offers.Remove(ctx, "alice", o.Label, AuthPassword); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if s, _ := b.shared.List(ctx, core.SessionOpen); len(s) != 0 {
		t.Fatalf("open sessions after the offer was removed: %+v", s)
	}
	if _, err := a.links.Connect(ctx, lead.Session.ID, "bob/new:trainer", core.PermTasksAuto, ""); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("a removed offer is no longer listed: %v", err)
	}
}

func TestOffersAreListedOnlyToTheirMachine(t *testing.T) {
	ctx := context.Background()
	n, a, b := linkNet(t)
	c := n.node("carol")
	n.pair(c, b)
	gpu := withOffers(t, b)
	gpu.offer(t, "alice", "trainer", core.PermTasksAuto, nil)
	if _, listed, err := c.discover.List(ctx, "bob"); err != nil || len(listed.Offers) != 0 {
		t.Fatalf("carol sees %+v, %v", listed.Offers, err)
	}
	_, listed, err := a.discover.List(ctx, "bob")
	if err != nil || len(listed.Offers) != 1 {
		t.Fatalf("alice sees %+v, %v", listed.Offers, err)
	}
	// carol names alice's offer: it looks missing.
	lead := shareOn(t, c, 1, "lead", core.Visibility{})
	now := n.clock.Now()
	l, err := c.st.InsertLink(ctx, store.Link{Peer: b.id, ID: core.NewID(), Direction: store.LinkOutbound, Session: lead.Session.ID,
		RemoteName: "new:trainer", PermissionIn: core.PermMessages, Proposed: core.PermMessages, State: store.LinkPending,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.sender.SendEnvelope(ctx, b.id, core.KindLinkRequest, "", core.LinkRequestBody{
		LinkID: l.ID, FromSession: core.SessionRef{ID: lead.Session.ID, Name: "lead"}, OfferID: listed.Offers[0].OfferID,
		ProposedPermission: core.PermMessages,
	}); err != nil {
		t.Fatal(err)
	}
	n.pump()
	if got := c.linkOf(t, b, l.ID); got.State != store.LinkClosed || got.Reason != core.RejectNotFound {
		t.Fatalf("carol's request %+v", got)
	}
	// Invalid offers in an answer are dropped.
	clean, ok := cleanListedOffer(core.ListedOffer{OfferID: "bad", Label: "trainer", MaxPermission: core.PermMessages})
	if ok {
		t.Fatalf("an offer without a valid ID passed: %+v", clean)
	}
	if _, ok := cleanListedOffer(core.ListedOffer{OfferID: core.NewID(), Label: "Bad Label", MaxPermission: core.PermMessages}); ok {
		t.Fatal("an invalid label passed")
	}
	if got, ok := cleanListedOffer(core.ListedOffer{OfferID: core.NewID(), Label: "x", Agent: "Evil‮", MaxPermission: core.PermMessages}); !ok || got.Agent != "" {
		t.Fatalf("agent not cleaned: %+v", got)
	}
}

func TestManagedSessionsStayOpenAcrossARestart(t *testing.T) {
	ctx := context.Background()
	st := d2Store(t)
	shared := NewSessionService(st, core.NewFakeClock(d2Epoch))
	dir := filepath.Join(t.TempDir(), "proj")
	m, err := shared.CreateManaged(ctx, "trainer-ab12", "managed session: trainer", dir)
	if err != nil {
		t.Fatal(err)
	}
	live := d2Share(t, shared, "lead")
	if err := shared.AwayAll(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := shared.Get(ctx, m.ID); s.State != core.SessionOpen {
		t.Fatalf("managed after restart: %s", s.State)
	}
	if s, _ := shared.Get(ctx, live.ID); s.State != core.SessionAway {
		t.Fatalf("live after restart: %s", s.State)
	}
	if _, err := shared.CreateManaged(ctx, "Bad Name", "", dir); !errors.Is(err, ErrBadSessionName) {
		t.Fatalf("bad name: %v", err)
	}
}
