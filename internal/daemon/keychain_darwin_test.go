package daemon

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeKeychain emulates security(1) for one generic password item.
type fakeKeychain struct {
	item    string
	broken  bool // every call fails with a non-"not found" status
	calls   []string
	addFail bool
	delFail bool // delete-generic-password fails with a non-"not found" status
}

func (f *fakeKeychain) run(_ context.Context, args ...string) ([]byte, int, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if f.broken {
		return nil, 51, errors.New("keychain locked")
	}
	switch args[0] {
	case "find-generic-password":
		if f.item == "" {
			return nil, keychainNotFound, errors.New("not found")
		}
		return []byte(f.item + "\n"), 0, nil
	case "add-generic-password":
		if f.addFail {
			return nil, 1, errors.New("denied")
		}
		for i, a := range args {
			if a == "-w" {
				f.item = args[i+1]
			}
		}
		return nil, 0, nil
	case "delete-generic-password":
		if f.delFail {
			return nil, 1, errors.New("denied")
		}
		if f.item == "" {
			return nil, keychainNotFound, errors.New("not found")
		}
		f.item = ""
		return nil, 0, nil
	}
	return nil, 1, errors.New("unexpected")
}

func TestKeychainIdentityStore(t *testing.T) {
	ctx := context.Background()
	fallback := SettingsIdentityStore{Settings: d2Store(t)}
	kc := &fakeKeychain{}
	ks := &KeychainIdentityStore{Fallback: fallback, run: kc.run}

	id, err := LoadOrCreateIdentity(ctx, ks)
	if err != nil {
		t.Fatal(err)
	}
	if kc.item != base64.StdEncoding.EncodeToString(id.Seed()) {
		t.Fatal("seed not stored in the keychain")
	}
	if _, found, _ := fallback.Load(ctx); found {
		t.Fatal("seed also left in the settings store")
	}
	if !strings.Contains(kc.calls[1], "-s cravv-connect -a identity") || !strings.HasSuffix(kc.calls[1], "-U") {
		t.Fatalf("add call = %q", kc.calls[1])
	}
	again, err := LoadOrCreateIdentity(ctx, ks)
	if err != nil || again.MachineID() != id.MachineID() {
		t.Fatalf("reload: %v", err)
	}

	// A locked keychain with nothing in the fallback must not mint a new identity.
	locked := &KeychainIdentityStore{Fallback: SettingsIdentityStore{Settings: d2Store(t)}, run: (&fakeKeychain{broken: true}).run}
	if _, err := LoadOrCreateIdentity(ctx, locked); err == nil {
		t.Fatal("locked keychain produced an identity")
	}

	// When the keychain refuses to store, the fallback keeps the seed.
	refusing := &fakeKeychain{addFail: true}
	fb := SettingsIdentityStore{Settings: d2Store(t)}
	rs := &KeychainIdentityStore{Fallback: fb, run: refusing.run}
	id2, err := LoadOrCreateIdentity(ctx, rs)
	if err != nil {
		t.Fatal(err)
	}
	if seed, found, _ := fb.Load(ctx); !found || base64.StdEncoding.EncodeToString(seed) != base64.StdEncoding.EncodeToString(id2.Seed()) {
		t.Fatal("fallback did not keep the seed")
	}
	if err := ks.Delete(ctx); err != nil || kc.item != "" {
		t.Fatalf("delete: %v item=%q", err, kc.item)
	}
}

// A locked Keychain can make security(1) wait for a dialog nobody sees (the
// daemon runs without a GUI session): every call is bounded, and a timeout is
// a clear error, never a silent new identity or a fallback write.
func TestKeychainTimeout(t *testing.T) {
	hang := func(ctx context.Context, _ ...string) ([]byte, int, error) {
		<-ctx.Done()
		return nil, -1, ctx.Err()
	}
	k := &KeychainIdentityStore{Fallback: SettingsIdentityStore{Settings: d2Store(t)}, run: hang, timeout: 20 * time.Millisecond}
	ctx := context.Background()
	start := time.Now()
	if _, found, err := k.Load(ctx); err == nil || found || !errors.Is(err, ErrKeychainTimeout) ||
		!strings.Contains(err.Error(), "unlock the login keychain") {
		t.Fatalf("Load = found %v, err %v", found, err)
	}
	if err := k.Save(ctx, []byte("seed")); !errors.Is(err, ErrKeychainTimeout) {
		t.Fatalf("Save err = %v", err)
	}
	if _, found, _ := k.Fallback.Load(ctx); found {
		t.Fatal("Save wrote the fallback after a Keychain timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("calls were not bounded by the timeout")
	}
	if d := NewKeychainIdentityStore(nil).timeout; d != 10*time.Second {
		t.Fatalf("default timeout %v", d)
	}
}

// A reset whose Keychain write fails must never come back as the old
// identity: the old item is removed before the seed goes to the fallback,
// and when it cannot be removed the Save fails.
func TestKeychainResetNeverRevertsToOldIdentity(t *testing.T) {
	ctx := context.Background()
	oldSeed, newSeed := []byte("old-seed"), []byte("new-seed")
	old := base64.StdEncoding.EncodeToString(oldSeed)

	kc := &fakeKeychain{item: old, addFail: true}
	fb := SettingsIdentityStore{Settings: d2Store(t)}
	ks := &KeychainIdentityStore{Fallback: fb, run: kc.run}
	if err := ks.Save(ctx, newSeed); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if kc.item != "" {
		t.Fatal("the old Keychain item survived a Save that fell back")
	}
	if seed, found, err := ks.Load(ctx); err != nil || !found || string(seed) != string(newSeed) {
		t.Fatalf("Load after fallback = %q %v %v, want the new seed", seed, found, err)
	}

	// The old item can be neither replaced nor removed: Save fails loudly.
	stuck := &fakeKeychain{item: old, addFail: true, delFail: true}
	sfb := SettingsIdentityStore{Settings: d2Store(t)}
	ss := &KeychainIdentityStore{Fallback: sfb, run: stuck.run}
	if err := ss.Save(ctx, newSeed); err == nil || !strings.Contains(err.Error(), "older identity") {
		t.Fatalf("Save with a stuck old item err = %v", err)
	}
	if _, found, _ := sfb.Load(ctx); found {
		t.Fatal("Save wrote the fallback while the old Keychain item stayed")
	}

	// State left by an older version: both hold a seed. The fallback is the
	// newer one (a successful Keychain write clears it), and the stale item goes.
	both := &fakeKeychain{item: old}
	bfb := SettingsIdentityStore{Settings: d2Store(t)}
	if err := bfb.Save(ctx, newSeed); err != nil {
		t.Fatal(err)
	}
	bs := &KeychainIdentityStore{Fallback: bfb, run: both.run}
	if seed, found, err := bs.Load(ctx); err != nil || !found || string(seed) != string(newSeed) {
		t.Fatalf("Load with both = %q %v %v, want the fallback seed", seed, found, err)
	}
	if both.item != "" {
		t.Fatal("the stale Keychain item was not removed")
	}
}
