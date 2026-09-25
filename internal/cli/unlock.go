package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

const passwordPrompt = "Login password for this machine: "

// maxPasswordTries bounds retries in one command; the daemon's lockout still
// applies across commands.
const maxPasswordTries = 3

// unlock asks for the login password and unlocks this connection.
func unlock(ctx context.Context, env *Env, c Caller) error {
	for try := 1; ; try++ {
		pw, err := env.Prompt.Password(passwordPrompt)
		if err != nil {
			return err
		}
		err = c.Call(ctx, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: pw}, nil)
		if err == nil {
			return nil
		}
		if !errors.Is(err, core.ErrBadPassword) || try == maxPasswordTries {
			return err
		}
		fmt.Fprintln(env.Stderr, "Incorrect password, try again.")
	}
}

// withUnlock runs fn; if the daemon says a password is required, it asks for
// it once and runs fn again. Commands never ask for a password the daemon does
// not need (for example, lowering trust).
func withUnlock(ctx context.Context, env *Env, c Caller, fn func() error) error {
	err := fn()
	if !errors.Is(err, core.ErrAuthRequired) {
		return err
	}
	if err := unlock(ctx, env, c); err != nil {
		return err
	}
	return fn()
}

// connect dials the daemon.
func connect(ctx context.Context, env *Env) (Caller, error) {
	return env.Dial(ctx)
}
