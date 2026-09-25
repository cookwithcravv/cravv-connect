package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func testPaths(t *testing.T) Paths {
	t.Helper()
	t.Setenv(EnvHome, filepath.Join(t.TempDir(), "state"))
	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolvePathsUsesCravvHomeWith0700(t *testing.T) {
	home := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvHome, home)
	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("home mode = %o, want 700", fi.Mode().Perm())
	}
	want := Paths{
		Home: home, Config: filepath.Join(home, "config.toml"), DB: filepath.Join(home, "store.db"),
		Audit: filepath.Join(home, "audit.log"), Socket: filepath.Join(home, "daemon.sock"),
		Files: filepath.Join(home, "files"), Log: filepath.Join(home, "daemon.log"),
	}
	if p != want {
		t.Fatalf("paths = %+v\nwant   %+v", p, want)
	}
}

func TestResolvePathsDefaultsToHomeDir(t *testing.T) {
	fake := t.TempDir()
	t.Setenv(EnvHome, "")
	t.Setenv("HOME", fake)
	p, err := ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if p.Home != filepath.Join(fake, ".cravv-connect") {
		t.Fatalf("Home = %q", p.Home)
	}
}

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	p := testPaths(t)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	wantPAM := "login"
	if runtime.GOOS == "darwin" {
		wantPAM = "chkpasswd"
	}
	if c.PeerQuota != core.DefaultPeerQuota || c.PAMService != wantPAM || c.RelayURL != "" || c.DeviceName == "" {
		t.Fatalf("defaults = %+v", c)
	}
	if strings.Contains(c.DeviceName, ".") {
		t.Fatalf("device name should be the short hostname, got %q", c.DeviceName)
	}
}

func TestLoadParsesStringsIntsAndComments(t *testing.T) {
	p := testPaths(t)
	content := `# top comment

relay_url = "https://relay.example.com"   # trailing comment
device_name="gpu \"box\" #1"
  peer_quota = 2_147_483_648
unknown_key = "ignored"
pam_service = "sudo"
`
	if err := os.WriteFile(p.Config, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := Config{RelayURL: "https://relay.example.com", DeviceName: `gpu "box" #1`, PeerQuota: 2147483648, PAMService: "sudo"}
	if c != want {
		t.Fatalf("Load = %+v, want %+v", c, want)
	}
}

func TestLoadRejectsMalformedLines(t *testing.T) {
	cases := map[string]string{
		"no equals":         "relay_url \"x\"\n",
		"unquoted string":   "relay_url = https://x\n",
		"unterminated":      "relay_url = \"https://x\n",
		"junk after string": "relay_url = \"x\" y\n",
		"non-integer":       "peer_quota = \"lots\"\n",
		"float":             "peer_quota = 1.5\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			p := testPaths(t)
			if err := os.WriteFile(p.Config, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(p)
			if err == nil {
				t.Fatalf("Load accepted %q", content)
			}
			if !strings.Contains(err.Error(), "line 1") {
				t.Fatalf("error does not name the line: %v", err)
			}
		})
	}
}

func TestSaveThenLoadRoundTripWith0600(t *testing.T) {
	p := testPaths(t)
	in := Config{RelayURL: "http://127.0.0.1:8787", DeviceName: "laptop \\ \"x\" # y", PeerQuota: 12345, PAMService: "login"}
	if err := Save(p, in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p.Config)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", fi.Mode().Perm())
	}
	out, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
	entries, _ := os.ReadDir(p.Home)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
