package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Claude Code allow rules (v2 spec 7.4). By default every tool that only
// reads or acts within an existing link is allowed, and so is the
// listener; the tools that open new flows or send local files prompt
// unless the user asks for them (--allow-send). Only rules cravv-connect
// added itself are ever removed (see allowStatePath).
var (
	claudeAllowedTools = []string{
		"machines", "sessions", "links", "check_inbox", "wait_for_message", "review_pending",
		"session_share", "session_close", "session_set", "disconnect", "restrict",
		"send_message", "get_task", "claim_task", "update_task", "complete_task", "fail_task", "cancel_task",
	}
	claudeSendTools = []string{"connect", "create_task", "send_file"}
)

func mcpRule(tool string) string { return "mcp__" + ServerName + "__" + tool }

// claudeAllowRules returns the rules to allow. The listener is allowed both
// as plain cravv-connect (the MCP server uses that name when PATH finds
// this binary) and by bin's path.
func claudeAllowRules(bin string, allowSend bool) []string {
	var rules []string
	for _, t := range claudeAllowedTools {
		rules = append(rules, mcpRule(t))
	}
	if allowSend {
		for _, t := range claudeSendTools {
			rules = append(rules, mcpRule(t))
		}
	}
	rules = append(rules, "Bash("+ServerName+" listen:*)")
	if q := shellQuote(bin); q != ServerName {
		rules = append(rules, "Bash("+q+" listen:*)")
	}
	return rules
}

// allowStatePath is the file that records the allow rules cravv-connect added to
// ~/.claude/settings.json, so install and uninstall never touch a rule the
// user wrote, even one that looks like ours.
func (c *Claude) allowStatePath() string {
	return filepath.Join(c.Home, ".cravv-connect", "claude-allow-rules.json")
}

// claudeAllowState is the state file: the allow rules we added and the
// IntegrationVersion that installed them (0: before versions were kept).
type claudeAllowState struct {
	Added   []string `json:"added"`
	Version int      `json:"version,omitempty"`
}

// readAllowState returns the state file (zero if there is none).
func (c *Claude) readAllowState() (claudeAllowState, error) {
	var st claudeAllowState
	b, err := os.ReadFile(c.allowStatePath())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s is not valid JSON, not changing the allow rules: %w", c.allowStatePath(), err)
	}
	return st, nil
}

// loadAllowState returns the rules we added (none if there is no state file).
func (c *Claude) loadAllowState() ([]string, error) {
	st, err := c.readAllowState()
	return st.Added, err
}

// removeAllowState removes the state file (uninstall).
func (c *Claude) removeAllowState() error {
	if err := os.Remove(c.allowStatePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// saveAllowState records added and this IntegrationVersion.
func (c *Claude) saveAllowState(added []string) error {
	path := c.allowStatePath()
	if added == nil {
		added = []string{}
	}
	b, err := json.MarshalIndent(claudeAllowState{Added: added, Version: IntegrationVersion}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600)
}

func isSendRule(rule string) bool {
	tool, ok := strings.CutPrefix(rule, "mcp__"+ServerName+"__")
	return ok && slices.Contains(claudeSendTools, tool)
}

// setClaudeAllow makes sure every rule in want is allowed and returns the
// rules we added, now. Of the rules we added earlier (added), those no
// longer wanted are removed, except send rules, which stay unless dropSend
// (install without --allow-send keeps them; --no-allow-send removes them).
// A rule already present that we did not add is the user's: it is left
// alone and never recorded as ours. Every other rule stays in place and in
// order.
func setClaudeAllow(settings map[string]any, want, added []string, dropSend bool) []string {
	ours := map[string]bool{}
	for _, r := range added {
		ours[r] = true
	}
	removeClaudeAllow(settings, func(r string) bool {
		return ours[r] && !slices.Contains(want, r) && (dropSend || !isSendRule(r))
	})
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	present := map[string]bool{}
	for _, r := range allow {
		if s, ok := r.(string); ok {
			present[s] = true
		}
	}
	var now []string
	for _, r := range allow {
		if s, ok := r.(string); ok && ours[s] && !slices.Contains(now, s) {
			now = append(now, s)
		}
	}
	for _, r := range want {
		if !present[r] {
			allow = append(allow, r)
			present[r] = true
			now = append(now, r)
		}
	}
	if len(allow) > 0 {
		perms["allow"] = allow
		settings["permissions"] = perms
	}
	return now
}

// removeClaudeAllow drops the string rules drop matches, then an allow list
// and a permissions object that became empty because of it.
func removeClaudeAllow(settings map[string]any, drop func(rule string) bool) {
	perms, ok := settings["permissions"].(map[string]any)
	if !ok {
		return
	}
	allow, ok := perms["allow"].([]any)
	if !ok {
		return
	}
	kept := make([]any, 0, len(allow))
	for _, r := range allow {
		if s, ok := r.(string); ok && drop(s) {
			continue
		}
		kept = append(kept, r)
	}
	if len(kept) == len(allow) {
		return
	}
	if len(kept) == 0 {
		delete(perms, "allow")
	} else {
		perms["allow"] = kept
	}
	if len(perms) == 0 {
		delete(settings, "permissions")
	}
}
