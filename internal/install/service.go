package install

import (
	"context"
	"errors"
)

// Service is the login service that keeps the daemon running.
type Service interface {
	Installed() bool
	Install(ctx context.Context, bin string) error
	Uninstall(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// ServiceConfig is what every service definition needs.
type ServiceConfig struct {
	Home      string // user's home directory
	CravvHome string // state directory, passed to the daemon as CRAVV_HOME
	// LogPath is the daemon's own log, rotated by the daemon (daemon run
	// --log-file). StderrPath only catches output written before logging
	// starts or a crash.
	LogPath    string
	StderrPath string
}

// ErrNoServiceManager is returned on systems without launchd or systemd.
var ErrNoServiceManager = errors.New("no supported service manager on this system; run `cravv-connect daemon run` yourself")
