package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

// SetupSystem is what `cravv-connect setup` needs from the network and the
// process table beyond Env. Tests replace it; nil in Env means realSystem.
type SetupSystem interface {
	// LookupHost resolves a host name (used for <host>.local).
	LookupHost(ctx context.Context, host string) ([]string, error)
	// PrivateAddrs returns this machine's private network addresses
	// (RFC 1918 and unique local), in interface order.
	PrivateAddrs() ([]netip.Addr, error)
	// RelayHealthy checks that a cravv relay answers GET /v1/health at origin.
	RelayHealthy(ctx context.Context, origin string) error
	// StartRelay starts bin in the background, in its own session, with
	// extraEnv added to this process's environment and its output appended
	// to logPath. A missing bin is an error matching fs.ErrNotExist.
	StartRelay(bin string, args, extraEnv []string, logPath string) error
}

func (env *Env) setupSystem() SetupSystem {
	if env.Setup != nil {
		return env.Setup
	}
	return realSystem{}
}

// realSystem is SetupSystem on the real network and process table.
type realSystem struct{}

func (realSystem) LookupHost(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

func (realSystem) PrivateAddrs() ([]netip.Addr, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if ok && ip.Unmap().IsPrivate() {
			out = append(out, ip.Unmap())
		}
	}
	return out, nil
}

// healthTimeout bounds one relay health check.
const healthTimeout = 5 * time.Second

func (realSystem) RelayHealthy(ctx context.Context, origin string) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+relayproto.PathHealth, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var h relayproto.Health
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&h) != nil || !h.OK {
		return errors.New("it does not answer like a cravv relay (GET /v1/health)")
	}
	return nil
}

func (realSystem) StartRelay(bin string, args, extraEnv []string, logPath string) error {
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", bin, err)
	}
	return cmd.Process.Release()
}
