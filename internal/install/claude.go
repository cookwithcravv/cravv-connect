package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// claudeHookEvents are the Claude Code hook events cravv-connect uses.
var claudeHookEvents = []string{"UserPromptSubmit", "Stop"}

// claudeHookTimeout is the per-hook timeout in seconds (the hook itself gives
// up after 2 seconds).
const claudeHookTimeout = 5

// Claude installs into Claude Code: the MCP server through the `claude` CLI
// (user scope) and the notice hooks in ~/.claude/settings.json.
type Claude struct {
	Home     string
	Run      Runner
	LookPath func(string) (string, error)
}

func (c *Claude) Name() string { return "claude" }

func (c *Claude) settingsPath() string { return filepath.Join(c.Home, ".claude", "settings.json") }

func (c *Claude) Detect() bool {
	if _, err := c.LookPath("claude"); err == nil {
		return true
	}
	return dirExists(filepath.Join(c.Home, ".claude"))
}

// Install registers the MCP server and merges the hooks. It adds the server
// first; only if that fails (usually because an entry already exists, for
// example with an old binary path) does it remove the entry and add it again,
// so a working registration is never removed when adding is impossible.
func (c *Claude) Install(ctx context.Context, bin string) error {
	if _, err := c.LookPath("claude"); err != nil {
		return errors.New("claude CLI not found on PATH; install Claude Code first, or see docs/agents.md for manual setup")
	}
	add := []string{"mcp", "add", "--scope", "user", ServerName, "--", bin, "mcp"}
	if _, err := c.Run.Run(ctx, "claude", add...); err != nil {
		if _, rmErr := c.Run.Run(ctx, "claude", "mcp", "remove", "--scope", "user", ServerName); rmErr != nil {
			return fmt.Errorf("claude mcp add failed and there was no entry to replace: %w", err)
		}
		if _, err := c.Run.Run(ctx, "claude", add...); err != nil {
			return fmt.Errorf("the old %s MCP entry was removed but adding the new one failed: %w; "+
				"run `claude %s` yourself, then run this install again", ServerName, err, strings.Join(add, " "))
		}
	}
	return c.editSettings(func(s map[string]any) { setClaudeHooks(s, shellQuote(bin)+" hook") })
}

// Uninstall removes the MCP server and only our hook entries.
func (c *Claude) Uninstall(ctx context.Context) error {
	if _, err := c.LookPath("claude"); err == nil {
		_, _ = c.Run.Run(ctx, "claude", "mcp", "remove", "--scope", "user", ServerName)
	}
	if _, err := os.Stat(c.settingsPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return c.editSettings(removeClaudeHooks)
}

// editSettings loads settings.json (or {}), applies fn, and writes it back.
// Invalid JSON is an error and the file is left untouched.
func (c *Claude) editSettings(fn func(map[string]any)) error {
	path := c.settingsPath()
	settings := map[string]any{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(b))) > 0 {
			if err := json.Unmarshal(b, &settings); err != nil {
				return fmt.Errorf("%s is not valid JSON, not changing it: %w", path, err)
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	fn(settings)
	// No HTML escaping: hook commands like `a && b` must stay readable.
	// Keys come out sorted (encoding/json sorts map keys).
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(settings); err != nil {
		return err
	}
	return writeFileAtomic(path, out.Bytes(), 0o600)
}

// isOurHook reports whether a hook command runs `<...>/cravv-connect hook`.
func isOurHook(cmd string) bool {
	rest, ok := strings.CutSuffix(strings.TrimSpace(cmd), " hook")
	if !ok {
		return false
	}
	rest = strings.Trim(rest, `'"`)
	return filepath.Base(rest) == ServerName
}

// setClaudeHooks replaces any cravv-connect hook entries with fresh ones.
func setClaudeHooks(settings map[string]any, command string) {
	removeClaudeHooks(settings)
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, ev := range claudeHookEvents {
		groups, _ := hooks[ev].([]any)
		hooks[ev] = append(groups, map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": claudeHookTimeout}},
		})
	}
	settings["hooks"] = hooks
}

// removeClaudeHooks drops our hook commands, then any groups and events that
// became empty because of it. Everything else is left as it was.
func removeClaudeHooks(settings map[string]any) {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return
	}
	for ev, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(groups))
		for _, g := range groups {
			group, ok := g.(map[string]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			list, ok := group["hooks"].([]any)
			if !ok {
				kept = append(kept, g)
				continue
			}
			var rest []any
			for _, h := range list {
				if hm, ok := h.(map[string]any); ok {
					if cmd, _ := hm["command"].(string); isOurHook(cmd) {
						continue
					}
				}
				rest = append(rest, h)
			}
			if len(rest) == 0 && len(list) > 0 {
				continue
			}
			group["hooks"] = rest
			kept = append(kept, group)
		}
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
}
