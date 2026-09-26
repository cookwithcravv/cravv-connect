package install

import (
	"path/filepath"
	"slices"
	"strings"
)

// Claude Code allow rules (v2 spec 7.4). By default every tool that only
// reads or acts within an existing link is allowed, and so is the
// listener; the tools that open new flows or send local files prompt
// unless the user asks for them (--allow-send).
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

// isOurAllowRule reports whether a rule is one cravv-connect adds.
func isOurAllowRule(rule string) bool {
	if tool, ok := strings.CutPrefix(rule, "mcp__"+ServerName+"__"); ok {
		return slices.Contains(claudeAllowedTools, tool) || slices.Contains(claudeSendTools, tool)
	}
	inner, ok := strings.CutPrefix(rule, "Bash(")
	if !ok {
		return false
	}
	prog, ok := strings.CutSuffix(inner, " listen:*)")
	return ok && filepath.Base(strings.Trim(prog, `'"`)) == ServerName
}

// setClaudeAllow replaces our allow rules with rules, keeping every other
// rule in place and in order.
func setClaudeAllow(settings map[string]any, rules []string) {
	removeClaudeAllow(settings)
	perms, _ := settings["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
	}
	allow, _ := perms["allow"].([]any)
	for _, r := range rules {
		allow = append(allow, r)
	}
	perms["allow"] = allow
	settings["permissions"] = perms
}

// removeClaudeAllow drops our allow rules, then an allow list and a
// permissions object that became empty because of it.
func removeClaudeAllow(settings map[string]any) {
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
		if s, ok := r.(string); ok && isOurAllowRule(s) {
			continue
		}
		kept = append(kept, r)
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
