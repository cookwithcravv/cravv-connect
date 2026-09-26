package e2e

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/cravv/cravv-connect/internal/daemon"
	"github.com/cravv/cravv-connect/internal/fakeagent"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// managedPair is a Mac and a GPU box, paired, where the GPU box offers the
// Mac managed sessions in a folder and runs the fake agent as claude.
type managedPair struct {
	mac, gpu *Node
	folder   string
	log      string
}

func newManagedPair(t *testing.T, mode string, offer ipc.OfferSetParams) managedPair {
	t.Helper()
	r := NewRelay(t)
	mac := NewNode(t, r, "mac", NodeOptions{AdminToken: AdminToken})
	gpu := NewNode(t, r, "gpu-box", NodeOptions{})
	Pair(t, mac, gpu)
	folder, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := managedPair{mac: mac, gpu: gpu, folder: folder, log: filepath.Join(t.TempDir(), "agent.log")}
	t.Setenv(daemon.EnvClaude, self)
	t.Setenv(fakeagent.EnvMode, mode)
	t.Setenv(fakeagent.EnvLog, p.log)
	t.Setenv(fakeagent.EnvSocket, gpu.Paths.Socket)
	offer.Machine, offer.Folder = "mac", folder
	Call(t, gpu.Unlocked(), ipc.MethodOffersSet, offer, nil)
	return p
}

// start connects a shared Mac chat to gpu-box/new:<label> and waits for the
// link to become active; it returns the Mac's link.
func (p managedPair) start(t *testing.T, lead *SharedChat, label, permission string) ipc.LinkView {
	t.Helper()
	out := Connect(t, lead, "gpu-box/new:"+label, permission, "")
	return p.mac.WaitLink(wait, "managed link active", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
}

func (p managedPair) runs(t *testing.T, n int) []fakeagent.Record {
	t.Helper()
	var recs []fakeagent.Record
	Eventually(t, wait, "fake agent runs", func() bool {
		var err error
		recs, err = fakeagent.Records(p.log)
		return err == nil && len(recs) >= n
	})
	return recs
}

func taskDone(t *testing.T, c *ipc.Client, id string) ipc.TaskView {
	t.Helper()
	var tv ipc.TaskView
	Eventually(t, wait, "task "+id+" finished", func() bool {
		Call(t, c, ipc.MethodTaskGet, ipc.TaskIDParams{TaskID: id}, &tv)
		return tv.State == "done" || tv.State == "failed"
	})
	return tv
}

// A Mac chat asks the GPU box for a managed session, gives it a task and a
// message, and gets the answers back, with nobody at the GPU box.
func TestManagedSessionEndToEnd(t *testing.T) {
	p := newManagedPair(t, "reply", ipc.OfferSetParams{Label: "trainer", Permission: "tasks-auto", RunMode: "edit-in-folder"})
	lead := p.mac.Share("claude", "lead", "private")
	var listed ipc.SessionsListResult
	Call(t, lead.C, ipc.MethodSessionsList, ipc.MachineParams{Machine: "gpu-box"}, &listed)
	if len(listed.Offers) != 1 || listed.Offers[0].Label != "trainer" || listed.Offers[0].MaxPermission != "tasks-auto" {
		t.Fatalf("the Mac sees offers %+v", listed.Offers)
	}
	link := p.start(t, lead, "trainer", "tasks-auto")
	if link.PermissionOut != "tasks-auto" || !strings.HasPrefix(link.RemoteSession, "trainer-") {
		t.Fatalf("Mac's link %+v", link)
	}

	var created ipc.TaskCreateResult
	Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: link.Link, Instructions: "count the lines in train.py"}, &created)
	tv := taskDone(t, lead.C, created.TaskID)
	if tv.State != "done" || !strings.Contains(tv.Wrapped, "done by the fake agent in "+p.folder) {
		t.Fatalf("task %+v", tv)
	}
	Call(t, lead.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: link.Link, Text: "thanks, anything else?"}, nil)
	WaitItem(t, lead.C, wait, "the managed session's reply", func(it ipc.InboxView) bool {
		return it.Kind == "chat" && strings.Contains(it.Wrapped, "fake agent read your message")
	})

	recs := p.runs(t, 2)
	var list ipc.ManagedListResult
	Call(t, p.gpu.Conn(), ipc.MethodManagedList, nil, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].Name != link.RemoteSession || list.Sessions[0].Machine != "mac" || !list.Sessions[0].Started {
		t.Fatalf("GPU box's managed sessions %+v", list.Sessions)
	}
	if recs[0].Dir != p.folder || recs[0].HasToken || recs[0].Token == "" || recs[0].Results["bind"] != "ok" || recs[0].Results["complete"] != "ok" {
		t.Fatalf("first run %+v", recs[0])
	}
	if !slices.Contains(recs[0].Args, "--session-id") || !slices.Contains(recs[1].Args, "--resume") {
		t.Fatalf("runs %q then %q", recs[0].Args, recs[1].Args)
	}

	// The Mac closes the link: the managed session closes on the GPU box.
	Call(t, lead.C, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: link.Link}, nil)
	Eventually(t, wait, "managed session closed", func() bool {
		Call(t, p.gpu.Conn(), ipc.MethodManagedList, nil, &list)
		return len(list.Sessions) == 0
	})
}

// A run's connection acts only as its own session: it cannot reach another
// managed session's task or link, share, connect, decide, unlock, change
// offers or use machine controls, and its token dies with the run.
func TestManagedRunTokenIsScoped(t *testing.T) {
	p := newManagedPair(t, "reply", ipc.OfferSetParams{Label: "trainer", Permission: "tasks-auto", MaxConcurrent: 2})
	lead := p.mac.Share("claude", "lead", "private")
	first := p.start(t, lead, "trainer", "tasks-auto")
	second := p.start(t, lead, "trainer", "tasks-auto")

	var other ipc.TaskCreateResult
	Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: second.Link, Instructions: "the second session's task"}, &other)
	taskDone(t, lead.C, other.TaskID)
	var gpuSecond ipc.LinkView
	for _, l := range p.gpu.AllLinks() {
		if l.Session == second.RemoteSession {
			gpuSecond = l
		}
	}
	probe, _ := json.Marshal(fakeagent.Probe{OtherTask: other.TaskID, OtherLink: gpuSecond.Link})
	t.Setenv(fakeagent.EnvProbe, string(probe))
	t.Setenv(fakeagent.EnvMode, "probe")

	var mine ipc.TaskCreateResult
	Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: first.Link, Instructions: "the first session's task"}, &mine)
	if tv := taskDone(t, lead.C, mine.TaskID); tv.State != "done" {
		t.Fatalf("the probing run's own task %+v", tv)
	}
	rec := p.runs(t, 2)[1]
	want := map[string]string{
		"register": "ok", "bind": "ok", "rebind": "ok", "links": "ok", "complete": "ok",
		"share": ipc.KindNotPermitted, "connect": ipc.KindNotPermitted, "decide": ipc.KindNotPermitted,
		"permit": ipc.KindNotPermitted, "offers": ipc.KindNotPermitted, "unlock": ipc.KindNotPermitted,
		"pause": ipc.KindNotPermitted, "kill": ipc.KindNotPermitted, "status": ipc.KindNotPermitted,
		"create_task": ipc.KindNotPermitted,
		"other_task":  ipc.KindNotFound, "other_link": ipc.KindNotFound,
	}
	for k, v := range want {
		if rec.Results[k] != v {
			t.Errorf("%s from a run: %q, want %q", k, rec.Results[k], v)
		}
	}
	if st := p.gpu.Status(); st.Killed || len(st.Peers) != 1 || st.Peers[0].Paused {
		t.Fatalf("the run changed the GPU box: %+v", st)
	}
	// The token died with its run.
	Eventually(t, wait, "the run ended", func() bool {
		var list ipc.ManagedListResult
		Call(t, p.gpu.Conn(), ipc.MethodManagedList, nil, &list)
		for _, s := range list.Sessions {
			if s.State != "idle" {
				return false
			}
		}
		return len(list.Sessions) == 2
	})
	c, _ := p.gpu.Session("claude")
	wantKind(t, TryCall(c, ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: rec.Token}, nil), ipc.KindNotFound)
}

// The kill switch stops a run (its whole process group) and closes the
// managed session; the Mac learns the task failed and the link closed.
func TestManagedKillStopsRuns(t *testing.T) {
	p := newManagedPair(t, "hang", ipc.OfferSetParams{Label: "trainer", Permission: "tasks-auto", RunMode: "shell", ShellConfirm: "shell"})
	lead := p.mac.Share("claude", "lead", "private")
	link := p.start(t, lead, "trainer", "tasks-auto")
	var created ipc.TaskCreateResult
	Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: link.Link, Instructions: "train for a week"}, &created)
	rec := p.runs(t, 1)[0]
	Call(t, p.gpu.Conn(), ipc.MethodKill, nil, nil)
	for _, pid := range []int{rec.PID, rec.ChildPID} {
		Eventually(t, wait, "the run's processes gone", func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
	}
	if tv := taskDone(t, lead.C, created.TaskID); tv.State != "failed" {
		t.Fatalf("task %+v", tv)
	}
	p.mac.WaitLink(wait, "link closed", func(l ipc.LinkView) bool { return l.Link == link.Link && l.State == "closed" })
	var list ipc.ManagedListResult
	Call(t, p.gpu.Conn(), ipc.MethodManagedList, nil, &list)
	if len(list.Sessions) != 0 {
		t.Fatalf("managed sessions after the kill switch: %+v", list.Sessions)
	}
}
