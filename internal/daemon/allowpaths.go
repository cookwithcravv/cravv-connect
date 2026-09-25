package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/pathguard"
	"github.com/cravv/cravv-connect/internal/store"
)

// AllowPaths holds the extra folders a human allowed for send_file
// (`cravv-connect allow-path <dir>`, password-gated) and
// checks outbound paths against them plus the session's project folder.
type AllowPaths struct {
	mu       sync.Mutex
	settings store.SettingsStore
	audit    audit.Logger
}

// NewAllowPaths builds an AllowPaths over the settings store.
func NewAllowPaths(settings store.SettingsStore, lg audit.Logger) *AllowPaths {
	if lg == nil {
		lg = audit.Nop{}
	}
	return &AllowPaths{settings: settings, audit: lg}
}

// List returns the allowed extra roots.
func (a *AllowPaths) List(ctx context.Context) ([]string, error) {
	raw, ok, err := a.settings.GetSetting(ctx, store.SettingAllowPaths)
	if err != nil || !ok || raw == "" {
		return nil, err
	}
	var roots []string
	if err := json.Unmarshal([]byte(raw), &roots); err != nil {
		return nil, fmt.Errorf("decode %s: %w", store.SettingAllowPaths, err)
	}
	return roots, nil
}

// Add allows an existing directory as an extra send_file root. It is a
// human-only action: unlocked must be true (set by the IPC layer after
// auth.unlock), otherwise core.ErrAuthRequired.
func (a *AllowPaths) Add(ctx context.Context, dir string, unlocked bool) error {
	if !unlocked {
		return core.ErrAuthRequired
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", real)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	roots, err := a.List(ctx)
	if err != nil {
		return err
	}
	if slices.Contains(roots, real) {
		return nil
	}
	roots = append(roots, real)
	b, err := json.Marshal(roots)
	if err != nil {
		return err
	}
	if err := a.settings.SetSetting(ctx, store.SettingAllowPaths, string(b)); err != nil {
		return err
	}
	_ = a.audit.Record(audit.Event{Type: audit.EvAllowPath, Detail: map[string]any{"path": real}})
	return nil
}

// Check applies pathguard's outbound rules with the current extra roots.
func (a *AllowPaths) Check(ctx context.Context, projectDir, path string) (string, os.FileInfo, error) {
	roots, err := a.List(ctx)
	if err != nil {
		return "", nil, err
	}
	return pathguard.NewOutbound(roots).Check(projectDir, path)
}

// Open applies the same rules as Check and opens the approved file without
// following symlinks, refusing a file with more than one hard link. The
// caller reads at most info.Size() bytes and closes the file.
func (a *AllowPaths) Open(ctx context.Context, projectDir, path string) (*os.File, os.FileInfo, error) {
	roots, err := a.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	return pathguard.NewOutbound(roots).Open(projectDir, path)
}
