package daemon

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/store"
)

const (
	keychainService  = "cravv-connect"
	keychainAccount  = "identity"
	keychainNotFound = 44 // security(1) exit status for errSecItemNotFound
)

// KeychainTimeout bounds every security(1) call. A locked login Keychain can
// make it wait for an unlock dialog that a daemon without a GUI session never
// shows.
const KeychainTimeout = 10 * time.Second

// ErrKeychainTimeout is returned when security(1) does not answer in time.
var ErrKeychainTimeout = errors.New("keychain locked or waiting for a dialog; unlock the login keychain " +
	"(security unlock-keychain ~/Library/Keychains/login.keychain-db) or start the daemon from your logged-in session")

// securityRunner runs /usr/bin/security and returns stdout and the exit status
// (-1 when the command could not run at all).
type securityRunner func(ctx context.Context, args ...string) ([]byte, int, error)

func runSecurity(ctx context.Context, args ...string) ([]byte, int, error) {
	out, err := exec.CommandContext(ctx, "/usr/bin/security", args...).Output()
	if err == nil {
		return out, 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out, ee.ExitCode(), err
	}
	return out, -1, err
}

// KeychainIdentityStore keeps the seed in the login Keychain (spec 4.4) and
// falls back to another store when the Keychain cannot be used. Note: the
// seed is passed to security(1) as an argument, so another process of the
// same user could see it in the process list for a moment; the threat model
// already treats same-user processes as trusted.
type KeychainIdentityStore struct {
	Fallback IdentityStore
	run      securityRunner
	timeout  time.Duration // per security(1) call; 0 means KeychainTimeout
}

// NewKeychainIdentityStore builds a Keychain store with a fallback.
func NewKeychainIdentityStore(fallback IdentityStore) *KeychainIdentityStore {
	return &KeychainIdentityStore{Fallback: fallback, run: runSecurity, timeout: KeychainTimeout}
}

// call runs security(1) with a deadline derived from ctx. When the deadline
// (not ctx itself) ends the call, the error is ErrKeychainTimeout.
func (k *KeychainIdentityStore) call(ctx context.Context, args ...string) ([]byte, int, error) {
	d := k.timeout
	if d <= 0 {
		d = KeychainTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	out, code, err := k.run(cctx, args...)
	if err != nil && ctx.Err() == nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return out, code, fmt.Errorf("%w (security %s did not answer within %s)", ErrKeychainTimeout, args[0], d)
	}
	return out, code, err
}

// DefaultIdentityStore is the Keychain with the settings store as fallback.
func DefaultIdentityStore(settings store.SettingsStore) IdentityStore {
	return NewKeychainIdentityStore(SettingsIdentityStore{Settings: settings})
}

// Load reads the Keychain item, then the fallback. A Keychain failure other
// than "not found" with nothing in the fallback is an error, never a silent
// new identity.
func (k *KeychainIdentityStore) Load(ctx context.Context) ([]byte, bool, error) {
	out, code, err := k.call(ctx, "find-generic-password", "-s", keychainService, "-a", keychainAccount, "-w")
	if err == nil {
		seed, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
		if derr != nil {
			return nil, false, fmt.Errorf("keychain identity: %w", derr)
		}
		return seed, true, nil
	}
	seed, found, ferr := k.Fallback.Load(ctx)
	if ferr != nil || found {
		return seed, found, ferr
	}
	if errors.Is(err, ErrKeychainTimeout) {
		return nil, false, err
	}
	if code != keychainNotFound {
		return nil, false, fmt.Errorf("keychain unavailable (unlock it or run the daemon in your login session): %w", err)
	}
	return nil, false, nil
}

// Save writes the Keychain item (updating it if present). If the Keychain
// refuses, the seed goes to the fallback instead; if it times out, Save fails.
func (k *KeychainIdentityStore) Save(ctx context.Context, seed []byte) error {
	b64 := base64.StdEncoding.EncodeToString(seed)
	if _, _, err := k.call(ctx, "add-generic-password", "-s", keychainService, "-a", keychainAccount, "-w", b64, "-U"); err != nil {
		if errors.Is(err, ErrKeychainTimeout) {
			return err // the Keychain may hold an older seed: do not split the identity
		}
		return k.Fallback.Save(ctx, seed)
	}
	return k.Fallback.Delete(ctx)
}

// Delete removes the Keychain item and the fallback copy.
func (k *KeychainIdentityStore) Delete(ctx context.Context) error {
	if _, code, err := k.call(ctx, "delete-generic-password", "-s", keychainService, "-a", keychainAccount); err != nil && code != keychainNotFound {
		return fmt.Errorf("keychain delete: %w", err)
	}
	return k.Fallback.Delete(ctx)
}
