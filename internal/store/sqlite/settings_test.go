package sqlite

import (
	"context"
	"testing"

	"github.com/cravv/cravv-connect/internal/store"
)

func TestSettings(t *testing.T) {
	ctx := context.Background()
	ss := newTestDB(t)
	if v, ok, err := ss.GetSetting(ctx, store.SettingKilled); err != nil || ok || v != "" {
		t.Fatalf("missing setting = %q %v %v", v, ok, err)
	}
	if err := ss.SetSetting(ctx, store.SettingKilled, "1"); err != nil {
		t.Fatal(err)
	}
	if err := ss.SetSetting(ctx, store.SettingAllowPaths, `["/data"]`); err != nil {
		t.Fatal(err)
	}
	if err := ss.SetSetting(ctx, store.SettingKilled, "0"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := ss.GetSetting(ctx, store.SettingKilled); err != nil || !ok || v != "0" {
		t.Fatalf("overwritten setting = %q %v %v", v, ok, err)
	}
	if v, _, _ := ss.GetSetting(ctx, store.SettingAllowPaths); v != `["/data"]` {
		t.Fatalf("allow_paths = %q", v)
	}
	// Empty string is a stored value, distinct from missing.
	if err := ss.SetSetting(ctx, "empty", ""); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := ss.GetSetting(ctx, "empty"); !ok || v != "" {
		t.Fatalf("empty setting = %q %v", v, ok)
	}
}
