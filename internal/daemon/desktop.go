package daemon

import (
	"os/exec"
	"runtime"
	"strings"
)

// NewDesktopNotifier returns an osascript notifier on macOS and a no-op elsewhere.
func NewDesktopNotifier() DesktopNotifier {
	if runtime.GOOS == "darwin" {
		return osascriptNotifier{run: func(script string) error {
			return exec.Command("/usr/bin/osascript", "-e", script).Run()
		}}
	}
	return nopDesktop{}
}

type nopDesktop struct{}

func (nopDesktop) Notify(title, text string) {}

// Available reports that nothing is shown (see DesktopAvailable).
func (nopDesktop) Available() bool { return false }

type osascriptNotifier struct{ run func(script string) error }

// Available reports that notifications are shown.
func (osascriptNotifier) Available() bool { return true }

// Notify shows the notification without blocking the caller.
func (n osascriptNotifier) Notify(title, text string) {
	script := "display notification " + appleScriptString(text) + " with title " + appleScriptString(title)
	go func() { _ = n.run(script) }()
}

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
