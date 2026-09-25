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
