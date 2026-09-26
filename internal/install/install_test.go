package install

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type fakeRunner struct {
	cmds     []string
	fail     map[string]bool // command prefix -> fail
	failNext map[string]int  // command prefix -> fail this many more times
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	r.cmds = append(r.cmds, cmd)
	for p, n := range r.failNext {
		if n > 0 && strings.HasPrefix(cmd, p) {
			r.failNext[p] = n - 1
			return "", errors.New("failed: " + cmd)
		}
	}
	for p := range r.fail {
		if strings.HasPrefix(cmd, p) {
			return "", errors.New("failed: " + cmd)
		}
	}
	return "", nil
}

func found(string) (string, error)   { return "/usr/local/bin/x", nil }
func missing(string) (string, error) { return "", errors.New("not found") }

var bg = context.Background()

const bin = "/usr/local/bin/cravv-connect"

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const existingSettings = `{
  "model": "opus",
  "permissions": {"allow": ["Bash(ls)"]},
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "say done"}]}],
    "PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "audit.sh"}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "/old/path/cravv-connect hook"}]}]
  }
}`

func TestClaudeInstallMergesAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(existingSettings), 0o644)
	r := &fakeRunner{}
	c := &Claude{Home: home, Run: r, LookPath: found}

	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("not idempotent:\n%s\n---\n%s", first, second)
	}
	// add succeeds (the fake has no existing entry): nothing is removed.
	wantCmds := []string{
		"claude mcp add --scope user cravv-connect -- " + bin + " mcp",
	}
	if !slices.Equal(r.cmds[:1], wantCmds) || len(r.cmds) != 2 {
		t.Fatalf("commands %v", r.cmds)
	}

	s := readJSON(t, path)
	if s["model"] != "opus" || s["permissions"] == nil {
		t.Fatalf("unrelated settings lost: %v", s)
	}
	hooks := s["hooks"].(map[string]any)
	if len(hooks["PreToolUse"].([]any)) != 1 {
		t.Fatal("PreToolUse changed")
	}
	stop := hooks["Stop"].([]any)
	if len(stop) != 2 || !strings.Contains(string(first), `"say done"`) {
		t.Fatalf("Stop groups %v", stop)
	}
	ups := hooks["UserPromptSubmit"].([]any)
	if len(ups) != 1 {
		t.Fatalf("old cravv-connect hook not replaced: %v", ups)
	}
	h := ups[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if h["command"] != bin+" hook" || h["type"] != "command" || h["timeout"] != float64(claudeHookTimeout) {
		t.Fatalf("hook %v", h)
	}
	if _, has := ups[0].(map[string]any)["matcher"]; has {
		t.Fatal("matcher must be omitted for UserPromptSubmit")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %v", fi.Mode().Perm())
	}
}

func TestClaudeUninstallKeepsOtherHooks(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(existingSettings), 0o600)
	r := &fakeRunner{}
	c := &Claude{Home: home, Run: r, LookPath: found}
	if err := c.Install(bg, "/Applications/My Tools/cravv-connect"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.cmds[0], "-- /Applications/My Tools/cravv-connect mcp") {
		t.Fatalf("add %q", r.cmds[0])
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `"'/Applications/My Tools/cravv-connect' hook"`) {
		t.Fatalf("path with space not quoted: %s", b)
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	s := readJSON(t, path)
	hooks := s["hooks"].(map[string]any)
	if _, ok := hooks["UserPromptSubmit"]; ok {
		t.Fatalf("UserPromptSubmit should be gone: %v", hooks)
	}
	if len(hooks["Stop"].([]any)) != 1 || len(hooks["PreToolUse"].([]any)) != 1 || s["model"] != "opus" {
		t.Fatalf("other settings changed: %v", s)
	}
	if r.cmds[len(r.cmds)-1] != "claude mcp remove --scope user cravv-connect" {
		t.Fatalf("commands %v", r.cmds)
	}
}

func TestClaudeFreshInstallAndUninstallLeavesNoHooksKey(t *testing.T) {
	home := t.TempDir()
	c := &Claude{Home: home, Run: &fakeRunner{}, LookPath: found}
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode %v", fi.Mode().Perm())
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	if s := readJSON(t, path); len(s) != 0 {
		t.Fatalf("leftovers %v", s)
	}
}

func TestClaudeRefusesInvalidJSONAndMissingCLI(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("{not json"), 0o600)
	c := &Claude{Home: home, Run: &fakeRunner{}, LookPath: found}
	if err := c.Install(bg, bin); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{not json" {
		t.Fatal("invalid file was modified")
	}
	c = &Claude{Home: home, Run: &fakeRunner{}, LookPath: missing}
	if err := c.Install(bg, bin); err == nil || !strings.Contains(err.Error(), "claude CLI not found") {
		t.Fatalf("err %v", err)
	}
}

func TestIsOurHook(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/usr/local/bin/cravv-connect hook":       true,
		"cravv-connect hook":                      true,
		"'/Apps/My Tools/cravv-connect' hook":     true,
		"/usr/local/bin/cravv-connect-dev hook":   false,
		"/usr/local/bin/cravv-connect mcp":        false,
		"say hook":                                false,
		"/usr/local/bin/cravv-connect hook --foo": false,
	} {
		if got := isOurHook(cmd); got != want {
			t.Errorf("isOurHook(%q) = %v", cmd, got)
		}
	}
}

const existingCodex = `model = "o3"

[mcp_servers.other]
command = "other"
args = ["serve"]

[mcp_servers.cravv-connect]
command = "/old/cravv-connect"
args = ["mcp"]

[mcp_servers.cravv-connect.env]
FOO = "bar"

[profiles.fast]
model = "o4-mini"
`

func TestCodexInstallIdempotentAndPreserving(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(existingCodex), 0o644)
	c := &Codex{Home: home, LookPath: found}
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("not idempotent:\n%s\n---\n%s", first, second)
	}
	want := `model = "o3"

[mcp_servers.other]
command = "other"
args = ["serve"]

[profiles.fast]
model = "o4-mini"

[mcp_servers.cravv-connect]
command = "/usr/local/bin/cravv-connect"
args = ["mcp"]
`
	if string(first) != want {
		t.Fatalf("got\n%s\nwant\n%s", first, want)
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if strings.Contains(string(after), "cravv-connect") || !strings.Contains(string(after), "[profiles.fast]") {
		t.Fatalf("uninstall:\n%s", after)
	}
}

func TestCodexFreshFileAndQuoting(t *testing.T) {
	home := t.TempDir()
	c := &Codex{Home: home, LookPath: found}
	if err := c.Install(bg, `C:\tools\"x"\cravv-connect`); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	want := "[mcp_servers.cravv-connect]\ncommand = \"C:\\\\tools\\\\\\\"x\\\"\\\\cravv-connect\"\nargs = [\"mcp\"]\n"
	if string(b) != want {
		t.Fatalf("%q\nwant %q", b, want)
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml")); len(b) != 0 {
		t.Fatalf("leftover %q", b)
	}
	if err := (&Codex{Home: t.TempDir(), LookPath: found}).Uninstall(bg); err != nil {
		t.Fatalf("uninstall without file: %v", err)
	}
}

func TestRegistryAndDetect(t *testing.T) {
	home := t.TempDir()
	reg := DefaultRegistry(home, &fakeRunner{})
	if !slices.Equal(reg.Names(), []string{"claude", "codex"}) {
		t.Fatal(reg.Names())
	}
	c := &Codex{Home: home, LookPath: missing}
	if c.Detect() {
		t.Fatal("detected without codex")
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
	if !c.Detect() {
		t.Fatal("not detected with ~/.codex")
	}
	if _, ok := reg.Get("cursor"); ok {
		t.Fatal("cursor has no installer")
	}
}

func TestLaunchd(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{}
	l := &Launchd{Cfg: ServiceConfig{Home: home, CravvHome: home + "/.cravv-connect", LogPath: home + "/.cravv-connect/daemon.log"}, Run: r, UID: 501}
	if l.Installed() {
		t.Fatal("installed before install")
	}
	if err := l.Install(bg, "/opt/a&b/cravv-connect"); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "dev.cravv.connect.plist"))
	if err != nil || !strings.Contains(string(plist), "<string>/opt/a&amp;b/cravv-connect</string>") ||
		!strings.Contains(string(plist), "<string>"+home+"/.cravv-connect</string>") {
		t.Fatalf("%v\n%s", err, plist)
	}
	want := []string{
		"launchctl bootout gui/501/dev.cravv.connect",
		"launchctl bootstrap gui/501 " + filepath.Join(home, "Library", "LaunchAgents", "dev.cravv.connect.plist"),
	}
	if !slices.Equal(r.cmds, want) {
		t.Fatalf("%v", r.cmds)
	}
	r.cmds, r.fail = nil, map[string]bool{"launchctl kickstart": true}
	if err := l.Start(bg); err != nil || len(r.cmds) != 2 || !strings.HasPrefix(r.cmds[1], "launchctl bootstrap") {
		t.Fatalf("start fallback: %v %v", err, r.cmds)
	}
	r.cmds, r.fail = nil, nil
	if err := l.Stop(bg); err != nil || r.cmds[0] != "launchctl bootout gui/501/dev.cravv.connect" {
		t.Fatalf("stop %v", r.cmds)
	}
	if err := l.Uninstall(bg); err != nil || l.Installed() {
		t.Fatalf("uninstall %v", err)
	}
}

func TestSystemd(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{}
	s := &Systemd{Cfg: ServiceConfig{Home: home, CravvHome: "/home/u/.cravv-connect", LogPath: "/home/u/.cravv-connect/daemon.log"}, Run: r}
	if err := s.Install(bg, "/usr/local/bin/cravv-connect"); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "cravv-connect.service"))
	for _, line := range []string{"ExecStart=/usr/local/bin/cravv-connect daemon run --log-file /home/u/.cravv-connect/daemon.log", "Environment=CRAVV_HOME=/home/u/.cravv-connect", "WantedBy=default.target"} {
		if !strings.Contains(string(unit), line+"\n") {
			t.Fatalf("missing %q in\n%s", line, unit)
		}
	}
	if !slices.Equal(r.cmds, []string{"systemctl --user daemon-reload", "systemctl --user enable --now cravv-connect.service"}) {
		t.Fatalf("%v", r.cmds)
	}
	if !strings.Contains(s.Unit("/opt/my tools/cravv-connect"), `ExecStart="/opt/my tools/cravv-connect" daemon run --log-file`) {
		t.Fatal(s.Unit("/opt/my tools/cravv-connect"))
	}
	r.cmds = nil
	if err := s.Uninstall(bg); err != nil || s.Installed() {
		t.Fatal(err)
	}
	if !slices.Equal(r.cmds, []string{"systemctl --user disable --now cravv-connect.service", "systemctl --user daemon-reload"}) {
		t.Fatalf("%v", r.cmds)
	}
}

func TestLaunchdLogsToRotatingFile(t *testing.T) {
	home := t.TempDir()
	l := &Launchd{Cfg: ServiceConfig{Home: home, CravvHome: home + "/.c", LogPath: home + "/.c/daemon.log", StderrPath: home + "/.c/daemon-stderr.log"}}
	plist := l.Plist("/usr/local/bin/cravv-connect")
	for _, want := range []string{
		"<string>run</string>\n    <string>--log-file</string>\n    <string>" + home + "/.c/daemon.log</string>",
		"<key>StandardErrorPath</key>\n  <string>" + home + "/.c/daemon-stderr.log</string>",
	} {
		if !strings.Contains(plist, want) {
			t.Fatalf("plist lacks %q:\n%s", want, plist)
		}
	}
	if strings.Contains(plist, "StandardOutPath") {
		t.Fatalf("stdout still appended to a file:\n%s", plist)
	}
}

func TestSystemdEscapesSpecifiersAndDollars(t *testing.T) {
	s := &Systemd{Cfg: ServiceConfig{CravvHome: "/home/u/100%$HOME", LogPath: "/home/u/100%$HOME/daemon.log"}}
	unit := s.Unit("/opt/50%off/$bin/cravv-connect")
	for _, want := range []string{
		"ExecStart=/opt/50%%off/$$bin/cravv-connect daemon run --log-file /home/u/100%%$$HOME/daemon.log\n",
		// Environment= expands specifiers but not variables: only % is doubled.
		"Environment=CRAVV_HOME=/home/u/100%%$HOME\n",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
}

func TestClaudeInstallReplacesExistingEntry(t *testing.T) {
	home := t.TempDir()
	add := "claude mcp add --scope user cravv-connect -- " + bin + " mcp"
	r := &fakeRunner{failNext: map[string]int{"claude mcp add": 1}} // already exists
	c := &Claude{Home: home, Run: r, LookPath: found}
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	want := []string{add, "claude mcp remove --scope user cravv-connect", add}
	if !slices.Equal(r.cmds, want) {
		t.Fatalf("commands %v", r.cmds)
	}
}

func TestClaudeInstallReportsFailedReAdd(t *testing.T) {
	home := t.TempDir()
	r := &fakeRunner{fail: map[string]bool{"claude mcp add": true}}
	c := &Claude{Home: home, Run: r, LookPath: found}
	err := c.Install(bg, bin)
	if err == nil || !strings.Contains(err.Error(), "claude mcp add --scope user cravv-connect -- "+bin+" mcp") ||
		!strings.Contains(err.Error(), "removed") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".claude", "settings.json")); !os.IsNotExist(statErr) {
		t.Fatal("hooks installed although the MCP server is not registered")
	}
}

func TestClaudeSettingsNotHTMLEscaped(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"test -f a && echo <done>"}]}]}}`), 0o600)
	c := &Claude{Home: home, Run: &fakeRunner{}, LookPath: found}
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"test -f a && echo <done>"`) {
		t.Fatalf("settings rewritten with HTML escapes:\n%s", b)
	}
}

func allowList(t *testing.T, path string) []string {
	t.Helper()
	perms, _ := readJSON(t, path)["permissions"].(map[string]any)
	var out []string
	for _, r := range perms["allow"].([]any) {
		out = append(out, r.(string))
	}
	return out
}

// v2 spec 7.4: the default allow rules, --allow-send, idempotent and
// reversible, other rules untouched.
func TestClaudeAllowRules(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte(existingSettings), 0o600)
	c := &Claude{Home: home, Run: &fakeRunner{}, LookPath: found}
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	allow := allowList(t, path)
	for _, want := range []string{"Bash(ls)", "mcp__cravv-connect__check_inbox", "mcp__cravv-connect__complete_task", "mcp__cravv-connect__review_pending",
		"mcp__cravv-connect__session_share", "Bash(cravv-connect listen:*)", "Bash(" + bin + " listen:*)"} {
		if !slices.Contains(allow, want) {
			t.Errorf("allow lacks %s: %v", want, allow)
		}
	}
	for _, not := range []string{"mcp__cravv-connect__connect", "mcp__cravv-connect__create_task", "mcp__cravv-connect__send_file", "mcp__cravv-connect__kill_switch"} {
		if slices.Contains(allow, not) {
			t.Errorf("%s is allowed by default", not)
		}
	}
	if allow[0] != "Bash(ls)" || len(allow) != 1+len(claudeAllowedTools)+2 {
		t.Fatalf("allow %v", allow)
	}
	if err := c.InstallWith(bg, bin, Options{AllowSend: true}); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := c.InstallWith(bg, bin, Options{AllowSend: true}); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatalf("not idempotent:\n%s\n---\n%s", first, second)
	}
	if allow := allowList(t, path); !slices.Contains(allow, "mcp__cravv-connect__connect") || !slices.Contains(allow, "mcp__cravv-connect__send_file") {
		t.Fatalf("--allow-send: %v", allow)
	}
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	if allow := allowList(t, path); !slices.Contains(allow, "mcp__cravv-connect__connect") {
		t.Fatalf("installing again without a flag must keep the send rules: %v", allow)
	}
	if err := c.InstallWith(bg, bin, Options{NoAllowSend: true}); err != nil {
		t.Fatal(err)
	}
	if allow := allowList(t, path); slices.Contains(allow, "mcp__cravv-connect__connect") || !slices.Contains(allow, "mcp__cravv-connect__links") {
		t.Fatalf("--no-allow-send: %v", allow)
	}
	if err := c.InstallWith(bg, bin, Options{AllowSend: true, NoAllowSend: true}); err == nil {
		t.Fatal("--allow-send with --no-allow-send")
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	if allow := allowList(t, path); !slices.Equal(allow, []string{"Bash(ls)"}) {
		t.Fatalf("after uninstall %v", allow)
	}
}

// Review focus: install and uninstall touch only the allow rules
// cravv-connect added itself (recorded in a state file), never rules the
// user wrote, even ones that look like ours.
func TestClaudeAllowRulesOnlyTouchWhatWeAdded(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o700)
	mine := []string{"Bash(ls)", "mcp__cravv-connect__links", "mcp__cravv-connect__send_file", "Bash(cravv-connect listen:*)"}
	b, _ := json.Marshal(map[string]any{"permissions": map[string]any{"allow": mine}})
	os.WriteFile(path, b, 0o600)
	c := &Claude{Home: home, Run: &fakeRunner{}, LookPath: found}
	if err := c.InstallWith(bg, "/old/bin/cravv-connect", Options{AllowSend: true}); err != nil {
		t.Fatal(err)
	}
	allow := allowList(t, path)
	if !slices.Equal(allow[:len(mine)], mine) || slices.Index(allow, "mcp__cravv-connect__links") != 1 {
		t.Fatalf("the user's rules moved or doubled: %v", allow)
	}
	state, err := os.ReadFile(filepath.Join(home, ".cravv-connect", "claude-allow-rules.json"))
	if err != nil {
		t.Fatalf("no state file: %v", err)
	}
	for _, r := range mine[1:] {
		if strings.Contains(string(state), `"`+r+`"`) {
			t.Fatalf("the user's rule %s is recorded as ours: %s", r, state)
		}
	}
	// A new binary path replaces the listener rule we added.
	if err := c.InstallWith(bg, bin, Options{NoAllowSend: true}); err != nil {
		t.Fatal(err)
	}
	allow = allowList(t, path)
	if slices.Contains(allow, "Bash(/old/bin/cravv-connect listen:*)") || !slices.Contains(allow, "Bash("+bin+" listen:*)") {
		t.Fatalf("stale listener rule: %v", allow)
	}
	if slices.Contains(allow, "mcp__cravv-connect__connect") || !slices.Contains(allow, "mcp__cravv-connect__send_file") {
		t.Fatalf("--no-allow-send must remove our send rules and keep the user's: %v", allow)
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	if allow := allowList(t, path); !slices.Equal(allow, mine) {
		t.Fatalf("after uninstall %v, want the user's %v", allow, mine)
	}
	if _, err := os.Stat(filepath.Join(home, ".cravv-connect", "claude-allow-rules.json")); !os.IsNotExist(err) {
		t.Fatalf("state file left: %v", err)
	}
}

// The /cravv skill is written by install and removed by uninstall; a skill
// the user wrote under the same name is never touched.
func TestClaudeSkill(t *testing.T) {
	home := t.TempDir()
	c := &Claude{Home: home, Run: &fakeRunner{}, LookPath: found}
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "skills", "cravv", "SKILL.md")
	b, err := os.ReadFile(path)
	if err != nil || string(b) != CravvSkill || !strings.HasPrefix(string(b), "---\nname: cravv\ndescription: ") {
		t.Fatalf("skill %q %v", b, err)
	}
	for _, must := range []string{"session_share", "run_in_background", "check_inbox", "review_pending", "Start the listener again", "connect(", "disconnect(", "machines()", "sessions(machine)", "Never guess a code"} {
		if !strings.Contains(CravvSkill, must) {
			t.Errorf("skill lacks %q", must)
		}
	}
	if strings.ContainsRune(CravvSkill, '\u2014') {
		t.Error("em dash in the skill")
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("skill folder left: %v", err)
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, []byte("my own cravv skill"), 0o600)
	if err := c.Install(bg, bin); err != nil {
		t.Fatal(err)
	}
	if err := c.Uninstall(bg); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "my own cravv skill" {
		t.Fatalf("the user's skill was changed: %q", b)
	}
}
