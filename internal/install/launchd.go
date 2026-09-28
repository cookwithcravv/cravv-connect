package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LaunchdLabel is the launchd job label.
const LaunchdLabel = "dev.cravv.connect"

// LaunchdUnloadWait bounds how long Stop waits for launchd to remove the
// job after bootout, which returns before launchd has finished.
const LaunchdUnloadWait = 10 * time.Second

// Launchd manages ~/Library/LaunchAgents/dev.cravv.connect.plist.
type Launchd struct {
	Cfg ServiceConfig
	Run Runner
	UID int
	// UnloadWait and Poll bound and pace the wait for an unloaded job;
	// zero means LaunchdUnloadWait and 100 ms.
	UnloadWait time.Duration
	Poll       time.Duration
}

func (l *Launchd) plistPath() string {
	return filepath.Join(l.Cfg.Home, "Library", "LaunchAgents", LaunchdLabel+".plist")
}

func (l *Launchd) domain() string  { return fmt.Sprintf("gui/%d", l.UID) }
func (l *Launchd) target() string  { return l.domain() + "/" + LaunchdLabel }
func (l *Launchd) Installed() bool { _, err := os.Stat(l.plistPath()); return err == nil }

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// stderrPath is where launchd sends stderr (crash output only).
func (l *Launchd) stderrPath() string {
	if l.Cfg.StderrPath != "" {
		return l.Cfg.StderrPath
	}
	return l.Cfg.LogPath + ".stderr"
}

// Plist renders the job definition. The daemon writes its own rotating log
// (--log-file); launchd only captures stderr, for crashes.
func (l *Launchd) Plist(bin string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>` + LaunchdLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + xmlEscape(bin) + `</string>
    <string>daemon</string>
    <string>run</string>
    <string>--log-file</string>
    <string>` + xmlEscape(l.Cfg.LogPath) + `</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>CRAVV_HOME</key>
    <string>` + xmlEscape(l.Cfg.CravvHome) + `</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardErrorPath</key>
  <string>` + xmlEscape(l.stderrPath()) + `</string>
</dict>
</plist>
`
}

// Install writes the plist and (re)loads the job, which starts the daemon.
func (l *Launchd) Install(ctx context.Context, bin string) error {
	if err := writeFileAtomic(l.plistPath(), []byte(l.Plist(bin)), 0o644); err != nil {
		return err
	}
	if err := l.Stop(ctx); err != nil {
		return err
	}
	_, err := l.Run.Run(ctx, "launchctl", "bootstrap", l.domain(), l.plistPath())
	return err
}

// Uninstall unloads the job and deletes the plist.
func (l *Launchd) Uninstall(ctx context.Context) error {
	_ = l.Stop(ctx)
	if err := os.Remove(l.plistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// loaded reports whether launchd has the job.
func (l *Launchd) loaded(ctx context.Context) bool {
	_, err := l.Run.Run(ctx, "launchctl", "print", l.target())
	return err == nil
}

// Start bootstraps the job, which starts the daemon (RunAtLoad). Only a job
// launchd still has loaded (a daemon that exited on its own) is kickstarted
// instead: kickstarting a job that is being unloaded does nothing.
func (l *Launchd) Start(ctx context.Context) error {
	if l.loaded(ctx) {
		_, err := l.Run.Run(ctx, "launchctl", "kickstart", l.target())
		return err
	}
	_, err := l.Run.Run(ctx, "launchctl", "bootstrap", l.domain(), l.plistPath())
	return err
}

// Stop unloads the job (KeepAlive would otherwise restart the daemon) and
// waits until launchd has removed it, so a Start right after finds it gone.
// A job that is not loaded is already stopped.
func (l *Launchd) Stop(ctx context.Context) error {
	if _, err := l.Run.Run(ctx, "launchctl", "bootout", l.target()); err != nil {
		if !l.loaded(ctx) {
			return nil
		}
		return err
	}
	wait, poll := l.UnloadWait, l.Poll
	if wait <= 0 {
		wait = LaunchdUnloadWait
	}
	if poll <= 0 {
		poll = 100 * time.Millisecond
	}
	deadline := time.Now().Add(wait)
	for l.loaded(ctx) {
		if time.Now().After(deadline) {
			return fmt.Errorf("launchd did not unload %s within %s", LaunchdLabel, wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	return nil
}
