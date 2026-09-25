//go:build linux

package install

// NewService returns the systemd user service.
func NewService(cfg ServiceConfig, run Runner) (Service, error) {
	return &Systemd{Cfg: cfg, Run: run}, nil
}
