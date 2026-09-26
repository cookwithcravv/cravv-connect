//go:build darwin || linux

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/fakeagent"
	"github.com/cravv/cravv-connect/internal/store"
)

// hostEnv is a machine that runs one managed session for peer gpu-box
// with the fake agent (this test binary) as claude.
type hostEnv struct {
	*d2TaskEnv
	offers *OfferService
	host   *SessionHost
	rules  FolderRules
	proj   string
	log    string
	self   string
	offer  store.Offer
	sess   store.SharedSession
	mlink  store.Link
	runner Runner
}

// newHostEnv starts the host with the fake agent in mode and an
// edit-in-folder tasks-auto offer (mutate changes it).
func newHostEnv(t *testing.T, mode string, mutate func(*OfferInput)) *hostEnv {
	t.Helper()
	e := &hostEnv{d2TaskEnv: d2Tasks(t, core.PermTasksAuto)}
	e.rules, e.proj = offerTree(t)
	var err error
	if e.self, err = os.Executable(); err != nil {
		t.Fatal(err)
	}
	e.log = filepath.Join(t.TempDir(), "agent.log")
	t.Setenv(fakeagent.EnvMode, mode)
	t.Setenv(fakeagent.EnvLog, e.log)
	e.offers = NewOfferService(e.st, offerPeers{e.st}, e.rules, e.clock, e.audit)
	if e.runner == nil {
		e.runner = ExecRunner{}
	}
	e.host = NewSessionHost(HostDeps{
		Offers: e.offers, Store: e.st, Sessions: e.shared, Inbox: e.inbox, Links: e.st, Peers: e.st,
		Tasks:   func() HostTasks { return e.tasks },
		Sender:  func() EnvelopeSender { return e.sender },
		Adapter: func() AgentAdapter { return ClaudeAdapter{Path: e.self} },
		Runner: runnerFunc(func(ctx context.Context, c AgentCommand, env []string, d time.Duration) RunOutcome {
			return e.runner.Run(ctx, c, env, d)
		}),
		RunDir: filepath.Join(e.rules.StateDir, "runs"), Self: "/usr/local/bin/cravv-connect", StateDir: e.rules.StateDir,
		Clock: e.clock, Audit: e.audit,
	})
	// Wired as the daemon wires them: reads send task.update{seen}, a
	// closed session closes its links, a closed link closes its session.
	e.inbox.AddReadObserver(e.tasks)
	e.shared.AddObserver(e.links)
	e.offers.AddObserver(e.host)
	e.links.AddCloseObserver(e.host)
	in := OfferInput{Peer: "gpu-box", Label: "trainer", Folder: e.proj, Permission: core.PermTasksAuto, RunMode: core.RunEditInFolder}
	if mutate != nil {
		mutate(&in)
	}
	ctx := context.Background()
	if e.offer, err = e.offers.Set(ctx, in, AuthPassword); err != nil {
		t.Fatal(err)
	}
	linkID := core.NewID()
	sess, perm, err := e.host.StartManaged(ctx, e.peer, e.offer.ID, linkID)
	if err != nil {
		t.Fatal(err)
	}
	e.sess = sess
	if e.mlink, err = e.st.InsertLink(ctx, store.Link{
		Peer: e.peer.MachineID, ID: linkID, Direction: store.LinkInbound, Session: sess.ID, RemoteSession: core.NewID(),
		RemoteName: "lead", PermissionIn: perm, PermissionOut: core.PermMessages, State: store.LinkActive,
		CreatedAt: d2Epoch, UpdatedAt: d2Epoch,
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

type runnerFunc func(ctx context.Context, c AgentCommand, env []string, d time.Duration) RunOutcome

func (f runnerFunc) Run(ctx context.Context, c AgentCommand, env []string, d time.Duration) RunOutcome {
	return f(ctx, c, env, d)
}

// start runs the host until the test ends.
func (e *hostEnv) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = e.host.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// task delivers a task from gpu-box on the managed session's link.
func (e *hostEnv) task(t *testing.T, instructions string) string {
	t.Helper()
	id := core.NewID()
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCreate, e.mlink.ID, core.TaskCreateBody{TaskID: id, Instructions: instructions})); err != nil {
		t.Fatal(err)
	}
	return id
}

// chat delivers a message from gpu-box on the managed session's link.
func (e *hostEnv) chat(t *testing.T, text string) {
	t.Helper()
	h := d2Gated(e.st, e.shared, e.replies, NewChatHandler(e.inbox), nil)
	if err := h.Handle(context.Background(), e.peer, d2Env(t, e.peer, core.KindChat, e.mlink.ID, core.ChatBody{Text: text})); err != nil {
		t.Fatal(err)
	}
}

// finished waits until task id reaches a final state and returns it.
func (e *hostEnv) finished(t *testing.T, id string) store.Task {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		tk := e.state(t, id)
		if tk.State == core.TaskDone || tk.State == core.TaskFailed {
			return tk
		}
		if time.Now().After(deadline) {
			t.Fatalf("task %s still %s", id, tk.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// runs waits until the fake agent logged n runs and returns them.
func (e *hostEnv) runs(t *testing.T, n int) []fakeagent.Record {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		recs, err := fakeagent.Records(e.log)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) >= n {
			return recs
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d runs, want %d", len(recs), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func lastNote(tk store.Task) string {
	if len(tk.Notes) == 0 {
		return ""
	}
	return tk.Notes[len(tk.Notes)-1].Text
}

func TestHostRunsItemsOneAtATimeAndResumes(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t, "ok", nil)
	e.start(t)
	m, _ := e.st.GetManaged(ctx, e.sess.ID)
	id := e.task(t, "count the lines in main.go")
	tk := e.finished(t, id)
	// The fake agent answers nothing: the task fails, and the sender is told.
	if tk.State != core.TaskFailed || !strings.HasPrefix(lastNote(tk), ReasonNoResult) {
		t.Fatalf("task %+v", tk)
	}
	e.chat(t, "are you there?")
	recs := e.runs(t, 2)
	first, second := recs[0], recs[1]
	wantFirst := []string{"-p", "--session-id", m.AgentSession, "--output-format", "json", "--restricted", "--strict-mcp-config", "--mcp-config"}
	if !slices.Equal(first.Args[:len(wantFirst)], wantFirst) {
		t.Fatalf("first run args %q", first.Args)
	}
	if !slices.Contains(first.Args, "acceptEdits") || !slices.Contains(first.Args, "Read,Glob,Grep,Edit,Write") {
		t.Fatalf("edit-in-folder rules missing: %q", first.Args)
	}
	if !slices.Contains(first.Environ, "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1") || !slices.Contains(first.Environ, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1") {
		t.Fatal("the adapter's environment did not reach the run")
	}
	if !slices.Equal(second.Args[1:3], []string{"--resume", m.AgentSession}) {
		t.Fatalf("second run args %q", second.Args)
	}
	if first.Dir != e.proj || first.HasToken || first.Token == "" || first.Env["CRAVV_HOME"] != e.rules.StateDir {
		t.Fatalf("first run saw dir %q token %q in-env %v env %v", first.Dir, first.Token, first.HasToken, first.Env)
	}
	if first.Token == second.Token {
		t.Fatal("every run gets its own token")
	}
	if !strings.Contains(first.Prompt, "count the lines in main.go") || !strings.Contains(first.Prompt, `task_id="`+id+`"`) ||
		!strings.Contains(first.Prompt, "<remote_message") || !strings.Contains(first.Prompt, "complete_task") {
		t.Fatalf("task prompt:\n%s", first.Prompt)
	}
	if !strings.Contains(second.Prompt, "are you there?") || !strings.Contains(second.Prompt, "send_message(link=") {
		t.Fatalf("chat prompt:\n%s", second.Prompt)
	}
	Eventually(t, "two audited runs", func() bool { return len(e.audit.ofType(EvManagedRun)) == 2 })
	for _, ev := range e.audit.ofType(EvManagedRun) {
		if ev.Detail["outcome"] != "ok" || ev.Detail["session"] != e.sess.Name {
			t.Fatalf("run audit %+v", ev)
		}
	}
	if m, _ := e.st.GetManaged(ctx, e.sess.ID); !m.Started {
		t.Fatal("the session must be marked started after a run")
	}
	if left, _ := os.ReadDir(filepath.Join(e.rules.StateDir, "runs")); len(left) != 0 {
		t.Fatalf("run configs left behind: %v", left)
	}
	var states []core.TaskState
	for _, u := range d2Updates(t, e.sender) {
		if u.TaskID == id {
			states = append(states, u.State)
		}
	}
	if !slices.Equal(states, []core.TaskState{core.TaskSeen, core.TaskClaimed, core.TaskFailed}) {
		t.Fatalf("the sender saw %v", states)
	}
}

func TestHostKillsARunAtItsTimeout(t *testing.T) {
	e := newHostEnv(t, "hang", func(in *OfferInput) { in.RunTimeout = time.Second })
	e.start(t)
	id := e.task(t, "train forever")
	tk := e.finished(t, id)
	if tk.State != core.TaskFailed || lastNote(tk) != ReasonRunTimeout {
		t.Fatalf("task %+v", tk)
	}
	rec := e.runs(t, 1)[0]
	waitGone(t, rec.PID, rec.ChildPID)
	Eventually(t, "the timeout audited", func() bool {
		ev := e.audit.ofType(EvManagedRun)
		return len(ev) == 1 && ev[0].Detail["outcome"] == "timeout"
	})
}

func TestHostCapsRuns(t *testing.T) {
	e := newHostEnv(t, "ok", func(in *OfferInput) { in.RunsPerHour, in.RunsPerDay = 2, 3 })
	e.start(t)
	for range 2 {
		e.finished(t, e.task(t, "work"))
	}
	over := e.finished(t, e.task(t, "one too many"))
	if over.State != core.TaskFailed || lastNote(over) != ReasonRateLimited {
		t.Fatalf("over the hourly cap: %+v", over)
	}
	e.chat(t, "hello?")
	e.chat(t, "still there?")
	Eventually(t, "the chats refused", func() bool { return len(e.audit.ofType(EvManagedRefused)) == 3 })
	// One notice per link an hour: two sides that both refuse cannot ping-pong.
	chats := e.sender.ofKind(core.KindChat)
	if len(chats) != 1 || chats[0].LinkID != e.mlink.ID || !strings.Contains(string(chats[0].Body), "rate_limited") {
		t.Fatalf("the sender of the messages was told %+v", chats)
	}
	if n := len(e.runs(t, 2)); n != 2 {
		t.Fatalf("%d runs, want 2", n)
	}
	e.clock.Advance(61 * time.Minute)
	if tk := e.finished(t, e.task(t, "next hour")); lastNote(tk) == ReasonRateLimited {
		t.Fatalf("a new hour allows runs again: %+v", tk)
	}
	if tk := e.finished(t, e.task(t, "over the day")); lastNote(tk) != ReasonRateLimited {
		t.Fatalf("over the daily cap: %+v", tk)
	}
	if n := len(e.runs(t, 3)); n != 3 {
		t.Fatalf("%d runs, want 3", n)
	}
}

func TestHostRefusesAFolderThatMoved(t *testing.T) {
	ctx := context.Background()
	var a, b, link string
	e := newHostEnv(t, "ok", func(in *OfferInput) {
		a, b, link = filepath.Join(in.Folder, "a"), filepath.Join(in.Folder, "b"), filepath.Join(in.Folder, "current")
		for _, d := range []string{a, b} {
			if err := os.Mkdir(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(a, link); err != nil {
			t.Fatal(err)
		}
		in.Folder = link
	})
	e.start(t)
	e.finished(t, e.task(t, "first"))
	if rec := e.runs(t, 1)[0]; rec.Dir != a {
		t.Fatalf("ran in %q, want %q", rec.Dir, a)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	tk := e.finished(t, e.task(t, "second"))
	if tk.State != core.TaskFailed || lastNote(tk) != ReasonFolderRefused {
		t.Fatalf("task after the swap %+v", tk)
	}
	Eventually(t, "the session and its link closed", func() bool {
		s, _ := e.shared.Get(ctx, e.sess.ID)
		l, _ := e.st.GetLink(ctx, e.peer.MachineID, e.mlink.ID)
		return s.State == core.SessionClosed && l.State == store.LinkClosed
	})
	if recs, _ := fakeagent.Records(e.log); len(recs) != 1 {
		t.Fatalf("no run may start in a moved folder: %d runs", len(recs))
	}
}

// The run token binds a connection to its own session for its own run only.
func TestHostRunTokenBindsOnlyItsSession(t *testing.T) {
	ctx := context.Background()
	var other store.SharedSession
	var e *hostEnv
	type seen struct{ bind, current, otherCurrent, otherBind, bogus error }
	got := make(chan seen, 1)
	var token string
	e = newHostEnv(t, "ok", func(in *OfferInput) { in.MaxConcurrent = 2 })
	e.runner = runnerFunc(func(ctx context.Context, c AgentCommand, env []string, d time.Duration) RunOutcome {
		cfg := fakeagentArg(c.Args, "--mcp-config")
		b, err := os.ReadFile(cfg)
		if err != nil {
			t.Error(err)
		}
		token = strings.Split(strings.Split(string(b), `"CRAVV_RUN_TOKEN": "`)[1], `"`)[0]
		var s seen
		_, s.bind = e.host.BindRun(ctx, token, 777)
		if _, err := os.Stat(cfg); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the run's MCP config outlived the bind: %v", err)
		}
		_, s.current = e.shared.Current(ctx, e.sess.ID, 777)
		_, s.otherCurrent = e.shared.Current(ctx, other.ID, 777)
		_, s.otherBind = e.shared.BindRun(ctx, other.ID, 777)
		_, s.bogus = e.host.BindRun(ctx, "not-a-token", 778)
		// The child answers as its own session.
		id := strings.Split(strings.Split(c.Stdin, `task_id="`)[1], `"`)[0]
		if _, err := e.tasks.Complete(ctx, e.sess.ID, "", id, "42 lines", nil); err != nil {
			t.Error(err)
		}
		got <- s
		return RunOutcome{Stdout: []byte(`{"type":"result","num_turns":1,"session_id":"x"}`)}
	})
	var err error
	if other, _, err = e.host.StartManaged(ctx, e.peer, e.offer.ID, core.NewID()); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	id := e.task(t, "count")
	s := <-got
	if s.bind != nil || s.current != nil {
		t.Fatalf("own session: bind %v, current %v", s.bind, s.current)
	}
	if !errors.Is(s.otherCurrent, core.ErrNotShared) || !errors.Is(s.otherBind, ErrAlreadyShared) || !errors.Is(s.bogus, core.ErrNotFound) {
		t.Fatalf("other session: current %v, bind %v; bogus token %v", s.otherCurrent, s.otherBind, s.bogus)
	}
	if tk := e.finished(t, id); tk.State != core.TaskDone || tk.Result != "42 lines" {
		t.Fatalf("task %+v", tk)
	}
	if _, err := e.host.BindRun(ctx, token, 779); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("a token after its run: %v", err)
	}
	if _, err := e.shared.Current(ctx, e.sess.ID, 777); !errors.Is(err, core.ErrNotShared) {
		t.Fatalf("the run's connection after the run: %v", err)
	}
}

func fakeagentArg(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestHostOpenHoldsTheQueue(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t, "ok", nil)
	e.start(t)
	if _, _, err := e.host.Open(ctx, e.sess.Name); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("open before any run: %v", err)
	}
	e.finished(t, e.task(t, "first"))
	Eventually(t, "the session started", func() bool {
		m, _ := e.st.GetManaged(ctx, e.sess.ID)
		return m.Started
	})
	m, _ := e.st.GetManaged(ctx, e.sess.ID)
	info, release, err := e.host.Open(ctx, e.sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Folder != e.proj || !slices.Equal(info.Command, []string{e.self, "--resume", m.AgentSession}) {
		t.Fatalf("open info %+v", info)
	}
	list, err := e.host.List(ctx)
	if err != nil || len(list) != 1 || !list[0].Live || list[0].Link != e.mlink.Num || list[0].Offer.Label != "trainer" || list[0].Alias != "gpu-box" {
		t.Fatalf("list %+v, %v", list, err)
	}
	id := e.task(t, "while open")
	time.Sleep(300 * time.Millisecond)
	if tk := e.state(t, id); tk.State != core.TaskQueued {
		t.Fatalf("a run started while the human had it open: %+v", tk)
	}
	release()
	release() // idempotent
	e.finished(t, id)
	if n := len(e.runs(t, 2)); n != 2 {
		t.Fatalf("%d runs", n)
	}
	if _, _, err := e.host.Open(ctx, "nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("unknown name: %v", err)
	}
}

func TestHostIdleSweepAndStop(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t, "hang", func(in *OfferInput) { in.IdleTimeout = time.Hour })
	e.start(t)
	id := e.task(t, "long job")
	rec := e.runs(t, 1)[0]
	e.clock.Advance(2 * time.Hour)
	if n, err := e.host.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("a running session is not idle: %d, %v", n, err)
	}
	e.host.StopAll()
	if tk := e.finished(t, id); lastNote(tk) != ReasonStopped {
		t.Fatalf("stopped task %+v", tk)
	}
	waitGone(t, rec.PID, rec.ChildPID)
	Eventually(t, "the worker ended", func() bool {
		e.host.mu.Lock()
		defer e.host.mu.Unlock()
		return !e.host.busy[e.sess.ID]
	})
	if n, err := e.host.Sweep(ctx); err != nil || n != 0 {
		t.Fatalf("the run just ended, so the session is not idle yet: %d, %v", n, err)
	}
	e.clock.Advance(61 * time.Minute)
	// A queue check may hold the session for a moment; the next sweep closes it.
	Eventually(t, "the idle sweep", func() bool {
		n, err := e.host.Sweep(ctx)
		return err == nil && n == 1
	})
	if l, _ := e.st.GetLink(ctx, e.peer.MachineID, e.mlink.ID); l.State != store.LinkClosed || l.Reason != core.CloseSessionClosed {
		t.Fatalf("the link must close with the session: %+v", l)
	}
	if closed := e.sender.ofKind(core.KindLinkClosed); len(closed) != 1 {
		t.Fatalf("the peer must be told: %+v", closed)
	}
}

func TestHostFailsInterruptedRuns(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t, "ok", nil)
	id := e.task(t, "claimed before a restart")
	if _, err := e.tasks.Claim(ctx, e.sess.ID, id); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	if tk := e.finished(t, id); lastNote(tk) != ReasonInterrupted {
		t.Fatalf("task %+v", tk)
	}
}

// Eventually polls cond every 20ms for up to 10 seconds.
func Eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A run gets only the allowlisted environment: never the daemon's CRAVV_*
// settings or secrets, never unrelated credentials, but what claude needs
// to authenticate.
func TestHostPassesAnAllowlistedEnvironment(t *testing.T) {
	t.Setenv("CRAVV_ADMIN_SECRET", "must-not-leak")
	t.Setenv("CRAVV_HOME", "/somewhere")
	t.Setenv("GITHUB_TOKEN", "ghp-must-not-leak")
	t.Setenv("ANTHROPIC_API_KEY", "sk-for-claude")
	t.Setenv("LC_ALL", "C")
	e := newHostEnv(t, "ok", nil)
	e.start(t)
	e.finished(t, e.task(t, "env"))
	rec := e.runs(t, 1)[0]
	vars := map[string]string{}
	for _, kv := range rec.Environ {
		k, v, _ := strings.Cut(kv, "=")
		vars[k] = v
	}
	for _, k := range []string{"CRAVV_ADMIN_SECRET", "CRAVV_HOME", "GITHUB_TOKEN", EnvRunToken} {
		if _, ok := vars[k]; ok {
			t.Errorf("%s reached the run", k)
		}
	}
	if vars["ANTHROPIC_API_KEY"] != "sk-for-claude" || vars["LC_ALL"] != "C" || vars["HOME"] == "" || vars["PATH"] == "" {
		t.Errorf("the run is missing HOME, PATH, LC_ALL or ANTHROPIC_API_KEY")
	}
	if vars["CLAUDE_CODE_DISABLE_CLAUDE_MDS"] != "1" {
		t.Error("the adapter's own variables must still reach the run")
	}
}

// Whether the agent has the conversation is learned from the agent, not
// guessed: a run whose flags do not match what the agent has is run again
// once the other way, and Started follows the agent.
func TestHostLearnsWhetherTheConversationExists(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name    string
		started bool // what the daemon believes
		exists  bool // what the agent has
	}{
		{"created by a run that timed out", false, true},
		{"lost by the agent", true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			sessions := t.TempDir()
			t.Setenv(fakeagent.EnvSessions, sessions)
			e := newHostEnv(t, "strict", nil)
			m, _ := e.st.GetManaged(ctx, e.sess.ID)
			m.Started = c.started
			if err := e.st.PutManaged(ctx, m); err != nil {
				t.Fatal(err)
			}
			if c.exists {
				if err := os.WriteFile(filepath.Join(sessions, m.AgentSession), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			e.start(t)
			e.chat(t, "hello")
			recs := e.runs(t, 2)
			first, second := "--session-id", "--resume"
			if c.started {
				first, second = second, first
			}
			if recs[0].Args[1] != first || recs[1].Args[1] != second || recs[1].Args[2] != m.AgentSession {
				t.Fatalf("runs %q then %q", recs[0].Args[:3], recs[1].Args[:3])
			}
			Eventually(t, "the run audited", func() bool {
				ev := e.audit.ofType(EvManagedRun)
				return len(ev) == 1 && ev[0].Detail["outcome"] == "ok"
			})
			if m, _ := e.st.GetManaged(ctx, e.sess.ID); !m.Started {
				t.Fatal("the conversation exists now")
			}
		})
	}
}

// A run that timed out does not count as having created the conversation.
func TestHostTimeoutDoesNotMarkStarted(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t, "hang", func(in *OfferInput) { in.RunTimeout = time.Second })
	e.start(t)
	e.finished(t, e.task(t, "slow"))
	if m, _ := e.st.GetManaged(ctx, e.sess.ID); m.Started {
		t.Fatal("a run that timed out before any result must not mark the session started")
	}
}

// Two managed sessions of one machine start runs at once under a daily
// cap of one: exactly one runs (the cap check and the run record are one
// transaction).
func TestHostDailyCapHoldsUnderConcurrentStarts(t *testing.T) {
	ctx := context.Background()
	e := newHostEnv(t, "ok", func(in *OfferInput) { in.RunsPerDay, in.MaxConcurrent = 1, 2 })
	linkID := core.NewID()
	sess, perm, err := e.host.StartManaged(ctx, e.peer, e.offer.ID, linkID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.st.InsertLink(ctx, store.Link{
		Peer: e.peer.MachineID, ID: linkID, Direction: store.LinkInbound, Session: sess.ID, RemoteSession: core.NewID(),
		RemoteName: "lead", PermissionIn: perm, PermissionOut: core.PermMessages, State: store.LinkActive,
		CreatedAt: d2Epoch, UpdatedAt: d2Epoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := e.task(t, "first")
	b := core.NewID()
	if err := e.handle(t, d2Env(t, e.peer, core.KindTaskCreate, second.ID, core.TaskCreateBody{TaskID: b, Instructions: "second"})); err != nil {
		t.Fatal(err)
	}
	e.start(t)
	limited := 0
	for _, id := range []string{a, b} {
		var tk store.Task
		if id == a {
			tk = e.finished(t, id)
		} else {
			deadline := time.Now().Add(20 * time.Second)
			for {
				tk, _ = e.tasks.Get(ctx, sess.ID, id)
				if tk.State == core.TaskDone || tk.State == core.TaskFailed || time.Now().After(deadline) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		if lastNote(tk) == ReasonRateLimited {
			limited++
		}
	}
	if limited != 1 {
		t.Fatalf("%d of two concurrent runs were refused under a daily cap of one", limited)
	}
	if n, _ := e.st.CountRuns(ctx, store.RunFilter{Peer: e.peer.MachineID}); n != 1 {
		t.Fatalf("%d runs recorded", n)
	}
}
