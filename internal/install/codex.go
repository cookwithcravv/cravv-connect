package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Codex installs the MCP server into ~/.codex/config.toml with a small
// line-based editor (no TOML dependency). It owns exactly one table:
// [mcp_servers.cravv-connect].
type Codex struct {
	Home     string
	LookPath func(string) (string, error)
}

func (c *Codex) Name() string { return "codex" }

func (c *Codex) configPath() string { return filepath.Join(c.Home, ".codex", "config.toml") }

func (c *Codex) Detect() bool {
	if _, err := c.LookPath("codex"); err == nil {
		return true
	}
	return dirExists(filepath.Join(c.Home, ".codex"))
}

var codexHeaders = []string{"[mcp_servers.cravv-connect]", `[mcp_servers."cravv-connect"]`}

// tomlString quotes s as a TOML basic string.
func tomlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	return `"` + r.Replace(s) + `"`
}

func codexTable(bin string) []string {
	return []string{
		codexHeaders[0],
		"command = " + tomlString(bin),
		`args = ["mcp"]`,
	}
}

// Install writes (or rewrites) our table and leaves every other line as is.
func (c *Codex) Install(_ context.Context, bin string) error {
	lines, err := c.read()
	if err != nil {
		return err
	}
	lines = removeTable(lines)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	lines = append(lines, codexTable(bin)...)
	return writeFileAtomic(c.configPath(), []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// Uninstall removes our table only.
func (c *Codex) Uninstall(context.Context) error {
	if _, err := os.Stat(c.configPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	lines, err := c.read()
	if err != nil {
		return err
	}
	lines = removeTable(lines)
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	out := ""
	if len(lines) > 0 {
		out = strings.Join(lines, "\n") + "\n"
	}
	return writeFileAtomic(c.configPath(), []byte(out), 0o600)
}

func (c *Codex) read() ([]string, error) {
	b, err := os.ReadFile(c.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

func isOurHeader(line string) bool {
	t := strings.TrimSpace(line)
	if i := strings.Index(t, "#"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	for _, h := range codexHeaders {
		if t == h {
			return true
		}
	}
	return false
}

func isHeader(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "[") }

// removeTable deletes our table: its header and every line up to the next
// table header. Sub-tables such as [mcp_servers.cravv-connect.env] go too.
func removeTable(lines []string) []string {
	out := make([]string, 0, len(lines))
	skipping := false
	for _, l := range lines {
		if isHeader(l) {
			t := strings.TrimSpace(l)
			skipping = isOurHeader(l) ||
				strings.HasPrefix(t, "[mcp_servers.cravv-connect.") ||
				strings.HasPrefix(t, `[mcp_servers."cravv-connect".`)
		}
		if !skipping {
			out = append(out, l)
		}
	}
	return out
}
