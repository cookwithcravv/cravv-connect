package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// SystemdUnit is the systemd user unit name.
const SystemdUnit = "cravv-connect.service"

// Systemd manages ~/.config/systemd/user/cravv-connect.service.
type Systemd struct {
	Cfg ServiceConfig
	Run Runner
}

func (s *Systemd) unitPath() string {
	return filepath.Join(s.Cfg.Home, ".config", "systemd", "user", SystemdUnit)
}

func (s *Systemd) Installed() bool { _, err := os.Stat(s.unitPath()); return err == nil }

// systemdQuote quotes a word for ExecStart and Environment lines.
func systemdQuote(v string) string {
	if !strings.ContainsAny(v, " \t\"\\'") {
		return v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}

// Unit renders the unit file. Output goes to the journal; the daemon also
// writes its own log in the state directory.
func (s *Systemd) Unit(bin string) string {
	return `[Unit]
Description=cravv-connect daemon
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=` + systemdQuote(bin) + ` daemon run
Environment=` + systemdQuote("CRAVV_HOME="+s.Cfg.CravvHome) + `
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`
}

func (s *Systemd) ctl(ctx context.Context, args ...string) error {
	_, err := s.Run.Run(ctx, "systemctl", append([]string{"--user"}, args...)...)
	return err
}

// Install writes the unit, then enables and starts it.
func (s *Systemd) Install(ctx context.Context, bin string) error {
	if err := writeFileAtomic(s.unitPath(), []byte(s.Unit(bin)), 0o644); err != nil {
		return err
	}
	if err := s.ctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	return s.ctl(ctx, "enable", "--now", SystemdUnit)
}

// Uninstall disables and removes the unit.
func (s *Systemd) Uninstall(ctx context.Context) error {
	_ = s.ctl(ctx, "disable", "--now", SystemdUnit)
	if err := os.Remove(s.unitPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.ctl(ctx, "daemon-reload")
}

func (s *Systemd) Start(ctx context.Context) error { return s.ctl(ctx, "start", SystemdUnit) }
func (s *Systemd) Stop(ctx context.Context) error  { return s.ctl(ctx, "stop", SystemdUnit) }
