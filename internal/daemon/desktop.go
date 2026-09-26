package daemon

import (
	"os/exec"
	"runtime"
	"strings"
)

// NewDesktopNotifier returns an osascript notifier on macOS and a no-op
// elsewhere. There is deliberately no Linux notifier: notify-send takes
// the text as arguments, and a notification may carry a confirmation code
// that must never appear on a command line (see osascriptNotifier).
func NewDesktopNotifier() DesktopNotifier {
	if runtime.GOOS == "darwin" {
		return newOsascriptNotifier(func(cmd *exec.Cmd) error { return cmd.Run() })
	}
	return nopDesktop{}
}

type nopDesktop struct{}

func (nopDesktop) Notify(title, text string) {}

// Available reports that nothing is shown (see DesktopAvailable).
func (nopDesktop) Available() bool { return false }

// osascriptNotifier shows notifications with osascript. The script, which
// holds the title and text (a confirmation code among them), goes to
// osascript on stdin, never in its arguments: every local process can read
// a command line with ps, so a model polling ps could otherwise read the
// code and approve on its own.
type osascriptNotifier struct{ run func(cmd *exec.Cmd) error }

func newOsascriptNotifier(run func(cmd *exec.Cmd) error) osascriptNotifier {
	return osascriptNotifier{run: run}
}

// Available reports that notifications are shown.
func (osascriptNotifier) Available() bool { return true }

// Notify shows the notification without blocking the caller.
func (n osascriptNotifier) Notify(title, text string) {
	cmd := osascriptCommand("display notification " + appleScriptString(text) + " with title " + appleScriptString(title))
	go func() { _ = n.run(cmd) }()
}

// osascriptCommand runs script with osascript, which reads a script from
// stdin when it gets no program argument.
func osascriptCommand(script string) *exec.Cmd {
	cmd := exec.Command("/usr/bin/osascript")
	cmd.Stdin = strings.NewReader(script)
	return cmd
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
