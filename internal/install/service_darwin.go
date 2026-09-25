//go:build darwin

package install

import "os"

// NewService returns the launchd service for this user.
func NewService(cfg ServiceConfig, run Runner) (Service, error) {
	return &Launchd{Cfg: cfg, Run: run, UID: os.Getuid()}, nil
}
