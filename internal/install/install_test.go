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
	cmds []string
	fail map[string]bool // command prefix -> fail
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	cmd := strings.Join(append([]string{name}, args...), " ")
	r.cmds = append(r.cmds, cmd)
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
	wantCmds := []string{
		"claude mcp remove --scope user cravv-connect",
		"claude mcp add --scope user cravv-connect -- " + bin + " mcp",
	}
	if !slices.Equal(r.cmds[:2], wantCmds) || len(r.cmds) != 4 {
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
	if !strings.Contains(r.cmds[1], "-- /Applications/My Tools/cravv-connect mcp") {
		t.Fatalf("add %q", r.cmds[1])
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
	s := &Systemd{Cfg: ServiceConfig{Home: home, CravvHome: "/home/u/.cravv-connect"}, Run: r}
	if err := s.Install(bg, "/usr/local/bin/cravv-connect"); err != nil {
		t.Fatal(err)
	}
	unit, _ := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "cravv-connect.service"))
	for _, line := range []string{"ExecStart=/usr/local/bin/cravv-connect daemon run", "Environment=CRAVV_HOME=/home/u/.cravv-connect", "WantedBy=default.target"} {
		if !strings.Contains(string(unit), line+"\n") {
			t.Fatalf("missing %q in\n%s", line, unit)
		}
	}
	if !slices.Equal(r.cmds, []string{"systemctl --user daemon-reload", "systemctl --user enable --now cravv-connect.service"}) {
		t.Fatalf("%v", r.cmds)
	}
	if !strings.Contains(s.Unit("/opt/my tools/cravv-connect"), `ExecStart="/opt/my tools/cravv-connect" daemon run`) {
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
