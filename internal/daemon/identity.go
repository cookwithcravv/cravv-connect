package daemon

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/cookwithcravv/cravv-connect/internal/keys"
	"github.com/cookwithcravv/cravv-connect/internal/store"
)

// IdentityStore keeps the 32-byte identity seed.
type IdentityStore interface {
	Load(ctx context.Context) (seed []byte, found bool, err error)
	Save(ctx context.Context, seed []byte) error
	Delete(ctx context.Context) error
}

// SettingsIdentityStore keeps the seed (base64) in store.SettingIdentitySeed.
// It is the Linux store and the macOS fallback when the Keychain is unusable.
type SettingsIdentityStore struct{ Settings store.SettingsStore }

// Load returns the stored seed, if any.
func (s SettingsIdentityStore) Load(ctx context.Context) ([]byte, bool, error) {
	v, ok, err := s.Settings.GetSetting(ctx, store.SettingIdentitySeed)
	if err != nil || !ok || v == "" {
		return nil, false, err
	}
	seed, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, false, fmt.Errorf("decode identity seed: %w", err)
	}
	return seed, true, nil
}

// Save stores the seed.
func (s SettingsIdentityStore) Save(ctx context.Context, seed []byte) error {
	return s.Settings.SetSetting(ctx, store.SettingIdentitySeed, base64.StdEncoding.EncodeToString(seed))
}

// Delete clears the seed.
func (s SettingsIdentityStore) Delete(ctx context.Context) error {
	return s.Settings.SetSetting(ctx, store.SettingIdentitySeed, "")
}

// LoadOrCreateIdentity loads the identity, generating and saving one on first run.
func LoadOrCreateIdentity(ctx context.Context, is IdentityStore) (*keys.Identity, error) {
	seed, found, err := is.Load(ctx)
	if err != nil {
		return nil, err
	}
	if found {
		return keys.IdentityFromSeed(seed)
	}
	id, err := keys.GenerateIdentity()
	if err != nil {
		return nil, err
	}
	if err := is.Save(ctx, id.Seed()); err != nil {
		return nil, err
	}
	return id, nil
}
