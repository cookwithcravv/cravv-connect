package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMCPProjectDir(t *testing.T) {
	env := &Env{Getwd: func() (string, error) { return "/work/glow-v2", nil }}
	if d, _ := mcpProjectDir(env, ""); d != "/work/glow-v2" {
		t.Fatal(d)
	}
	if d, _ := mcpProjectDir(env, "/srv/app/../api"); d != "/srv/api" {
		t.Fatal(d)
	}
}

func TestAgentSessionFromEnv(t *testing.T) {
	env := map[string]string{"CLAUDE_SESSION_ID": "old", "CLAUDE_CODE_SESSION_ID": "d7f5456e-cc64-487c-aa67-8839a2db4980"}
	if got := agentSessionFromEnv(func(k string) string { return env[k] }); got != "d7f5456e-cc64-487c-aa67-8839a2db4980" {
		t.Fatal(got)
	}
	delete(env, "CLAUDE_CODE_SESSION_ID")
	if got := agentSessionFromEnv(func(k string) string { return env[k] }); got != "old" {
		t.Fatal(got)
	}
	if got := agentSessionFromEnv(func(string) string { return "" }); got != "" {
		t.Fatal(got)
	}
}

func TestListenerProgram(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "cravv-connect")
	other := filepath.Join(dir, "other")
	for _, p := range []string{self, other} {
		if err := os.WriteFile(p, []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := &Env{Executable: func() (string, error) { return self, nil }}
	if got := listenerProgram(env, func(string) (string, error) { return self, nil }); got != "cravv-connect" {
		t.Fatalf("on PATH: %q", got)
	}
	if got := listenerProgram(env, func(string) (string, error) { return other, nil }); got != self {
		t.Fatalf("another binary on PATH: %q", got)
	}
	if got := listenerProgram(env, func(string) (string, error) { return "", errors.New("not found") }); got != self {
		t.Fatalf("not on PATH: %q", got)
	}
	if got := listenerProgram(&Env{}, nil); got != "cravv-connect" {
		t.Fatalf("no executable: %q", got)
	}
}
