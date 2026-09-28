package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/config"
	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// fakePrompter replays scripted answers and records every prompt.
type fakePrompter struct {
	passwords []string
	lines     []string
	asked     []string
}

func (p *fakePrompter) Password(prompt string) (string, error) {
	p.asked = append(p.asked, "password: "+prompt)
	if len(p.passwords) == 0 {
		return "", errors.New("unexpected password prompt")
	}
	pw := p.passwords[0]
	p.passwords = p.passwords[1:]
	return pw, nil
}

func (p *fakePrompter) Line(prompt, def string) (string, error) {
	p.asked = append(p.asked, "line: "+prompt)
	if len(p.lines) == 0 {
		return "", errors.New("unexpected prompt: " + prompt)
	}
	l := p.lines[0]
	p.lines = p.lines[1:]
	if l == "" {
		return def, nil
	}
	return l, nil
}

type recorded struct {
	Method string
	Params json.RawMessage
}

// fakeDaemon is a real ipc.Server over a temp socket with scripted handlers.
// auth.unlock accepts the password "pw".
type fakeDaemon struct {
	t     *testing.T
	srv   *ipc.Server
	mu    sync.Mutex
	calls []recorded
	sock  string
	home  string
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	dir, err := os.MkdirTemp("", "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	fd := &fakeDaemon{t: t, srv: ipc.NewServer(ipc.Options{}), sock: filepath.Join(dir, "d.sock"), home: dir}
	fd.handle(ipc.MethodAuthUnlock, ipc.GateAllowWhenKilled, func(cs *ipc.ConnState, p json.RawMessage) (any, error) {
		var up ipc.UnlockParams
		json.Unmarshal(p, &up)
		if up.Password != "pw" {
			return nil, core.ErrBadPassword
		}
		return ipc.UnlockResult{ExpiresAt: cs.Unlock(core.UnlockTTL)}, nil
	})
	fd.handle(ipc.MethodSessionRegister, ipc.GateNone, func(cs *ipc.ConnState, p json.RawMessage) (any, error) {
		var sp ipc.SessionRegisterParams
		json.Unmarshal(p, &sp)
		name := sp.Agent + "@" + filepath.Base(sp.ProjectDir)
		cs.SetSession(name, sp.ProjectDir)
		return ipc.SessionRegisterResult{Name: name}, nil
	})
	return fd
}

// start begins serving; call after every handle.
func (fd *fakeDaemon) start() {
	ln, err := ipc.Listen(fd.sock)
	if err != nil {
		fd.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { fd.srv.Serve(ctx, ln); close(done) }()
	fd.t.Cleanup(func() { cancel(); <-done })
}

func (fd *fakeDaemon) handle(method string, gate ipc.Gate, fn func(cs *ipc.ConnState, p json.RawMessage) (any, error)) {
	fd.srv.Register(method, func(_ context.Context, cs *ipc.ConnState, p json.RawMessage) (any, error) {
		fd.mu.Lock()
		fd.calls = append(fd.calls, recorded{method, append(json.RawMessage(nil), p...)})
		fd.mu.Unlock()
		return fn(cs, p)
	}, gate)
}

func (fd *fakeDaemon) reply(method string, gate ipc.Gate, result any) {
	fd.handle(method, gate, func(*ipc.ConnState, json.RawMessage) (any, error) { return result, nil })
}

func (fd *fakeDaemon) methods() []string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	var out []string
	for _, c := range fd.calls {
		out = append(out, c.Method)
	}
	return out
}

func (fd *fakeDaemon) params(method string) string {
	fd.mu.Lock()
	defer fd.mu.Unlock()
	for i := len(fd.calls) - 1; i >= 0; i-- {
		if fd.calls[i].Method == method {
			return string(fd.calls[i].Params)
		}
	}
	return ""
}

type run struct {
	code   int
	stdout string
	stderr string
	prompt *fakePrompter
}

func (fd *fakeDaemon) env(p *fakePrompter, stdin string) (*Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	env := &Env{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb, Prompt: p,
		Paths: func() (config.Paths, error) {
			return config.Paths{Home: fd.home, Config: filepath.Join(fd.home, "config.toml"), DB: filepath.Join(fd.home, "store.db"),
				Socket: fd.sock, Log: filepath.Join(fd.home, "daemon.log")}, nil
		},
		Getwd:    func() (string, error) { return "/work/glow-v2", nil },
		Hostname: func() (string, error) { return "Alice's MacBook", nil },
		OpenSettings: func(string) (store.SettingsStore, func() error, error) {
			return nil, nil, errors.New("not used")
		},
	}
	env.Dial = func(ctx context.Context) (Caller, error) { return ipc.DialContext(ctx, fd.sock) }
	return env, &out, &errb
}

func (fd *fakeDaemon) run(p *fakePrompter, args ...string) run {
	return fd.runStdin(p, "", args...)
}

func (fd *fakeDaemon) runStdin(p *fakePrompter, stdin string, args ...string) run {
	if p == nil {
		p = &fakePrompter{}
	}
	env, out, errb := fd.env(p, stdin)
	code := Main(args, env)
	return run{code: code, stdout: out.String(), stderr: errb.String(), prompt: p}
}
