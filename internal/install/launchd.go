package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LaunchdLabel is the launchd job label.
const LaunchdLabel = "dev.cravv.connect"

// Launchd manages ~/Library/LaunchAgents/dev.cravv.connect.plist.
type Launchd struct {
	Cfg ServiceConfig
	Run Runner
	UID int
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

// Plist renders the job definition.
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
  <key>StandardOutPath</key>
  <string>` + xmlEscape(l.Cfg.LogPath) + `</string>
  <key>StandardErrorPath</key>
  <string>` + xmlEscape(l.Cfg.LogPath) + `</string>
</dict>
</plist>
`
}

// Install writes the plist and (re)loads the job, which starts the daemon.
func (l *Launchd) Install(ctx context.Context, bin string) error {
	if err := writeFileAtomic(l.plistPath(), []byte(l.Plist(bin)), 0o644); err != nil {
		return err
	}
	_, _ = l.Run.Run(ctx, "launchctl", "bootout", l.target())
	_, err := l.Run.Run(ctx, "launchctl", "bootstrap", l.domain(), l.plistPath())
	return err
}

// Uninstall unloads the job and deletes the plist.
func (l *Launchd) Uninstall(ctx context.Context) error {
	_, _ = l.Run.Run(ctx, "launchctl", "bootout", l.target())
	if err := os.Remove(l.plistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Start loads the job if needed, otherwise restarts it.
func (l *Launchd) Start(ctx context.Context) error {
	if _, err := l.Run.Run(ctx, "launchctl", "kickstart", l.target()); err == nil {
		return nil
	}
	_, err := l.Run.Run(ctx, "launchctl", "bootstrap", l.domain(), l.plistPath())
	return err
}

// Stop unloads the job (KeepAlive would otherwise restart the daemon).
func (l *Launchd) Stop(ctx context.Context) error {
	_, err := l.Run.Run(ctx, "launchctl", "bootout", l.target())
	return err
}
