package daemon

import (
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAppleScriptString(t *testing.T) {
	cases := map[string]string{
		"plain":            `"plain"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash`:       `"back\\slash"`,
		"line\nbreak\x07!": `"line break !"`,
	}
	for in, want := range cases {
		if got := appleScriptString(in); got != want {
			t.Errorf("appleScriptString(%q) = %s, want %s", in, got, want)
		}
	}
}

// Review focus: the notification text (and so a confirmation code) never
// reaches osascript's argv, where any local process could read it with ps.
func TestOsascriptNotifierKeepsTextOutOfArgv(t *testing.T) {
	got := make(chan *exec.Cmd, 1)
	n := newOsascriptNotifier(func(cmd *exec.Cmd) error { got <- cmd; return nil })
	n.Notify("cravv-connect code 4821", "Code 4821: link request.")
	var cmd *exec.Cmd
	select {
	case cmd = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("osascript was not run")
	}
	if cmd.Path != "/usr/bin/osascript" || len(cmd.Args) != 1 {
		t.Fatalf("command %q %q: the script must come on stdin, with no arguments", cmd.Path, cmd.Args)
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, "4821") {
			t.Fatalf("the code is in argv: %q", cmd.Args)
		}
	}
	if cmd.Stdin == nil {
		t.Fatal("no script on stdin")
	}
	script, _ := io.ReadAll(cmd.Stdin)
	if want := `display notification "Code 4821: link request." with title "cravv-connect code 4821"`; string(script) != want {
		t.Fatalf("script %q, want %q", script, want)
	}
}
