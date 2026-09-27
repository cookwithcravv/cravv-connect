//go:build !darwin

package daemon

import "github.com/cookwithcravv/cravv-connect/internal/store"

// DefaultIdentityStore is the settings store on systems without a Keychain.
func DefaultIdentityStore(settings store.SettingsStore) IdentityStore {
	return SettingsIdentityStore{Settings: settings}
}
