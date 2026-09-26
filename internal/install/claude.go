package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// claudeHookEvents are the Claude Code hook events cravv-connect uses.
var claudeHookEvents = []string{"UserPromptSubmit", "Stop"}

// claudeHookTimeout is the per-hook timeout in seconds (the hook itself gives
// up after 2 seconds).
const claudeHookTimeout = 5

// Claude installs into Claude Code: the MCP server through the `claude` CLI
// (user scope), the Stop and UserPromptSubmit hooks and the allow rules in
// ~/.claude/settings.json, and the /cravv skill in ~/.claude/skills/cravv.
type Claude struct {
	Home     string
	Run      Runner
	LookPath func(string) (string, error)
	// ConfigDir is CLAUDE_CONFIG_DIR when set: where Claude Code keeps
	// .claude.json instead of the home folder.
	ConfigDir string
}

func (c *Claude) Name() string { return "claude" }

func (c *Claude) settingsPath() string { return filepath.Join(c.Home, ".claude", "settings.json") }

func (c *Claude) Detect() bool {
	if _, err := c.LookPath("claude"); err == nil {
		return true
	}
	return dirExists(filepath.Join(c.Home, ".claude"))
}

// Install is InstallWith the default options.
func (c *Claude) Install(ctx context.Context, bin string) error {
	return c.InstallWith(ctx, bin, Options{})
}

// InstallWith registers the MCP server, merges the hooks and allow rules
// and writes the /cravv skill. It adds the server first; only if that fails
// (usually because an entry already exists, for example with an old binary
// path) does it remove the entry and add it again, so a working
// registration is never removed when adding is impossible. Running it again
// with the same options changes nothing.
func (c *Claude) InstallWith(ctx context.Context, bin string, o Options) error {
	if o.AllowSend && o.NoAllowSend {
		return errors.New("--allow-send and --no-allow-send cannot be used together")
	}
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
	added, err := c.loadAllowState()
	if err != nil {
		return err
	}
	if err := c.editSettings(func(s map[string]any) {
		setClaudeHooks(s, shellQuote(bin)+" hook")
		added = setClaudeAllow(s, claudeAllowRules(bin, o.AllowSend), added, o.NoAllowSend)
	}); err != nil {
		return err
	}
	if err := c.saveAllowState(added); err != nil {
		return err
	}
	return c.writeSkill()
}

// Uninstall removes the MCP server, our hook entries, the skill and only
// the allow rules we added.
func (c *Claude) Uninstall(ctx context.Context) error {
	if _, err := c.LookPath("claude"); err == nil {
		_, _ = c.Run.Run(ctx, "claude", "mcp", "remove", "--scope", "user", ServerName)
	}
	if err := c.removeSkill(); err != nil {
		return err
	}
	added, err := c.loadAllowState()
	if err != nil {
		return err
	}
	if _, err := os.Stat(c.settingsPath()); errors.Is(err, os.ErrNotExist) {
		return c.removeAllowState()
	}
	if err := c.editSettings(func(s map[string]any) {
		removeClaudeHooks(s)
		removeClaudeAllow(s, func(r string) bool { return slices.Contains(added, r) })
	}); err != nil {
		return err
	}
	return c.removeAllowState()
}

// IntegrationVersion is the version of what InstallWith puts into Claude
// Code (the MCP server entry, the hooks, the allow rules and the /cravv
// skill). Bump it when any of them changes, so `setup` updates an older
// install without asking. It is recorded in the allow-rules state file.
const IntegrationVersion = 2

// integration is what is in place in Claude Code.
type integration struct {
	mcp, hooks bool
	skillOK    bool // our skill as this version writes it, or one the user wrote
	ourSkill   bool // a skill file of ours, of any version
	staleSkill bool // a skill file of ours that is not this version's
	version    int  // from the state file (0: none, or from before versions)
}

func (c *Claude) inspect() integration {
	var in integration
	in.mcp = c.mcpRegistered()
	in.hooks = c.hooksPresent()
	if b, err := os.ReadFile(c.skillPath()); err == nil {
		ours := strings.Contains(string(b), skillMarker)
		in.ourSkill = ours
		in.staleSkill = ours && string(b) != CravvSkill
		in.skillOK = !ours || !in.staleSkill
	}
	if st, err := c.readAllowState(); err == nil {
		in.version = st.Version
	}
	return in
}

// Installed reports whether this version's integration is in place: the
// MCP server registered for the user, both hooks, the /cravv skill (this
// version's, or one the user wrote), and this IntegrationVersion recorded.
// setup skips Claude Code then.
func (c *Claude) Installed() bool {
	in := c.inspect()
	return in.mcp && in.hooks && in.skillOK && in.version >= IntegrationVersion
}

// Outdated reports whether an older integration is in place (from an
// earlier version: an older version recorded, or none, or an older /cravv
// skill of ours). setup updates it without asking. A piece the user
// removed from a current install is not outdated: setup asks.
func (c *Claude) Outdated() bool {
	in := c.inspect()
	if in.mcp && in.hooks && in.skillOK && in.version >= IntegrationVersion {
		return false
	}
	return (in.mcp || in.hooks || in.ourSkill) && (in.version < IntegrationVersion || in.staleSkill)
}

// claudeUserConfig is where the claude CLI keeps user-scope MCP servers.
func (c *Claude) claudeUserConfig() string {
	if c.ConfigDir != "" {
		return filepath.Join(c.ConfigDir, ".claude.json")
	}
	return filepath.Join(c.Home, ".claude.json")
}

// mcpRegistered reports whether ~/.claude.json lists our MCP server for
// the user (what `claude mcp add --scope user` writes). Reading the file
// avoids `claude mcp get`, which starts the server to check it.
func (c *Claude) mcpRegistered() bool {
	b, err := os.ReadFile(c.claudeUserConfig())
	if err != nil {
		return false
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return false
	}
	_, ok := cfg.MCPServers[ServerName]
	return ok
}

// hooksPresent reports whether settings.json runs our hook on every event
// we use.
func (c *Claude) hooksPresent() bool {
	b, err := os.ReadFile(c.settingsPath())
	if err != nil {
		return false
	}
	var s struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	for _, ev := range claudeHookEvents {
		found := false
		for _, g := range s.Hooks[ev] {
			for _, h := range g.Hooks {
				found = found || isOurHook(h.Command)
			}
		}
		if !found {
			return false
		}
	}
	return true
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
