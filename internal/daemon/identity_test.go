package daemon

import (
	"bytes"
	"context"
	"testing"
)

func TestLoadOrCreateIdentityIsStable(t *testing.T) {
	ctx := context.Background()
	is := SettingsIdentityStore{Settings: d2Store(t)}
	first, err := LoadOrCreateIdentity(ctx, is)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateIdentity(ctx, is)
	if err != nil {
		t.Fatal(err)
	}
	if first.MachineID() != second.MachineID() || !bytes.Equal(first.Seed(), second.Seed()) {
		t.Fatal("identity changed between loads")
	}
	if err := is.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := is.Load(ctx); found {
		t.Fatal("seed still present after Delete")
	}
}
