//go:build !darwin && !linux

package install

// NewService reports that this system has no supported service manager.
func NewService(ServiceConfig, Runner) (Service, error) { return nil, ErrNoServiceManager }
