package daemon

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Output limits for one run: the agent's result is small JSON, and stderr
// is kept only for the log.
const (
	MaxRunStdout = 1 << 20
	MaxRunStderr = 4 << 10
	// runWaitDelay bounds how long a finished run waits for processes it
	// left behind that still hold its output pipes.
	runWaitDelay = 5 * time.Second
)

// RunOutcome is how an agent process ended.
type RunOutcome struct {
	Stdout   []byte
	Stderr   string // the last MaxRunStderr bytes
	ExitCode int    // -1 when it did not exit on its own
	TimedOut bool   // killed at the run timeout
	Stopped  bool   // killed because ctx ended (close, kill switch, shutdown)
	Err      error  // it could not be started
	Duration time.Duration
}

// Runner starts agent processes. ExecRunner is the real one.
type Runner interface {
	Run(ctx context.Context, cmd AgentCommand, env []string, timeout time.Duration) RunOutcome
}

// ExecRunner runs each command in a process group of its own and kills the
// whole group (the agent and everything it started) at the timeout or when
// ctx ends.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, cmd AgentCommand, env []string, timeout time.Duration) RunOutcome {
	start := time.Now()
	c := exec.Command(cmd.Path, cmd.Args...)
	c.Dir, c.Env = cmd.Dir, env
	c.Stdin = strings.NewReader(cmd.Stdin)
	stdout, stderr := &capBuffer{max: MaxRunStdout}, &capBuffer{max: MaxRunStderr, tail: true}
	c.Stdout, c.Stderr = stdout, stderr
	c.WaitDelay = runWaitDelay
	ownGroup(c)
	if err := c.Start(); err != nil {
		return RunOutcome{Err: err, ExitCode: -1}
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var out RunOutcome
	var err error
	select {
	case err = <-done:
	case <-timer.C:
		out.TimedOut = true
		killGroup(c)
		err = <-done
	case <-ctx.Done():
		out.Stopped = true
		killGroup(c)
		err = <-done
	}
	out.Stdout, out.Stderr, out.Duration = stdout.bytes(), string(stderr.bytes()), time.Since(start)
	out.ExitCode = c.ProcessState.ExitCode()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) && !errors.Is(err, exec.ErrWaitDelay) {
		out.Err = err
	}
	return out
}

// capBuffer keeps at most max bytes: the first ones, or the last ones when tail.
type capBuffer struct {
	mu   sync.Mutex
	buf  []byte
	max  int
	tail bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tail {
		b.buf = append(b.buf, p...)
		if len(b.buf) > b.max {
			b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
		}
		return len(p), nil
	}
	if room := b.max - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (b *capBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf...)
}
