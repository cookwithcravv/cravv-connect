package cli

import "testing"

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
