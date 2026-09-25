package daemon

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// fakeKeychain emulates security(1) for one generic password item.
type fakeKeychain struct {
	item    string
	broken  bool // every call fails with a non-"not found" status
	calls   []string
	addFail bool
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
