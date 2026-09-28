package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The tests of relay-cf/scripts/deploy.sh run it against fake node, npm,
// npx (wrangler) and curl, whose state lives in files under the fake dir.
// Nothing reaches Cloudflare.

// fakeNpx plays wrangler. Files in $FAKE: loggedin, bucket, rules, secret,
// subdomain, and the failure switches no_r2 and no_subdomain.
const fakeNpx = `#!/bin/sh
echo "$*" >> "$FAKE/calls"
[ "$1" = wrangler ] || exit 99
shift
case "$*" in
  "whoami --json") [ -e "$FAKE/loggedin" ] ;;
  login) touch "$FAKE/loggedin" ;;
  "r2 bucket info "*) [ -e "$FAKE/bucket" ] ;;
  "r2 bucket create "*)
    if [ -e "$FAKE/no_r2" ]; then echo "Please enable R2 through the Cloudflare Dashboard. [code: 10042]"; exit 1; fi
    touch "$FAKE/bucket" ;;
  "r2 bucket lifecycle list "*) cat "$FAKE/rules" 2>/dev/null; true ;;
  "r2 bucket lifecycle add "*) echo "name: expire-blobs" >> "$FAKE/rules" ;;
  deploy*)
    if [ -e "$FAKE/no_subdomain" ]; then echo "You need a workers.dev subdomain in order to proceed. [code: 10063]"; exit 1; fi
    echo "Uploaded cravv-relay"
    echo "  https://cravv-relay.$(cat "$FAKE/subdomain").workers.dev" ;;
  "secret put ADMIN_TOKEN") cat > "$FAKE/secret"; echo "Success! Uploaded secret ADMIN_TOKEN" ;;
  *) exit 98 ;;
esac
`

const fakeNpm = `#!/bin/sh
echo "npm $*" >> "$FAKE/calls"
mkdir -p node_modules/.bin
printf '#!/bin/sh\n' > node_modules/.bin/wrangler
chmod +x node_modules/.bin/wrangler
touch node_modules/.package-lock.json
`

const fakeNode = `#!/bin/sh
echo "v${FAKE_NODE:-22.3.0}"
`

const fakeCurl = `#!/bin/sh
echo "curl $*" >> "$FAKE/calls"
[ -e "$FAKE/unhealthy" ] && exit 22
echo '{"ok":true,"version":1}'
`

type deployRig struct {
	t     *testing.T
	fake  string // fake tools' state
	relay string // a copy of relay-cf with the script
	home  string
}

func newDeployRig(t *testing.T) *deployRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the deploy script is for macOS and Linux")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	r := &deployRig{t: t, fake: t.TempDir(), relay: t.TempDir(), home: t.TempDir()}
	bin := filepath.Join(r.fake, "bin")
	os.MkdirAll(bin, 0o755)
	for name, body := range map[string]string{"npx": fakeNpx, "npm": fakeNpm, "node": fakeNode, "curl": fakeCurl} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile("../relay-cf/scripts/deploy.sh")
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(r.relay, "scripts"), 0o755)
	os.WriteFile(filepath.Join(r.relay, "scripts", "deploy.sh"), script, 0o755)
	os.WriteFile(filepath.Join(r.relay, "package-lock.json"), []byte("{}"), 0o644)
	r.set("subdomain", "alice")
	return r
}

func (r *deployRig) set(name, content string) {
	if err := os.WriteFile(filepath.Join(r.fake, name), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *deployRig) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.fake, name))
	return string(b)
}

func (r *deployRig) tokenFile() string { return filepath.Join(r.home, ".cravv-relay-admin-token") }

// run runs the script and returns its exit code, its combined output, and
// the wrangler and npm calls it made.
func (r *deployRig) run(env ...string) (int, string, []string) {
	r.t.Helper()
	os.Remove(filepath.Join(r.fake, "calls"))
	cmd := exec.Command("bash", filepath.Join(r.relay, "scripts", "deploy.sh"))
	cmd.Env = append([]string{
		"HOME=" + r.home,
		"PATH=" + filepath.Join(r.fake, "bin") + ":/usr/bin:/bin",
		"FAKE=" + r.fake,
	}, env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		r.t.Fatal(err)
	}
	calls := strings.Split(strings.TrimSpace(r.read("calls")), "\n")
	return code, out.String(), calls
}

func count(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

const aliceURL = "https://cravv-relay.alice.workers.dev"

func TestRelayDeployFromScratch(t *testing.T) {
	r := newDeployRig(t)
	code, out, calls := r.run()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{
		"npm ci --no-audit --no-fund",
		"wrangler login",
		"wrangler r2 bucket create cravv-relay-blobs",
		"wrangler r2 bucket lifecycle add cravv-relay-blobs expire-blobs blobs/ --expire-days 15 --force",
		"wrangler deploy --var PUBLIC_ORIGIN:" + aliceURL,
		"wrangler secret put ADMIN_TOKEN",
		"curl -fsS " + aliceURL + "/v1/health",
	} {
		if count(calls, want) != 1 {
			t.Errorf("want one call %q, calls:\n%s", want, strings.Join(calls, "\n"))
		}
	}
	// The first deploy learns the URL, the second pins it.
	if count(calls, "wrangler deploy") != 2 {
		t.Errorf("want two deploys, calls:\n%s", strings.Join(calls, "\n"))
	}

	info, err := os.Stat(r.tokenFile())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode %v, want 0600", info.Mode().Perm())
	}
	token, _ := os.ReadFile(r.tokenFile())
	if !regexp.MustCompile(`^[0-9a-f]{64}\n$`).Match(token) {
		t.Errorf("token file holds %q, want 64 hex digits", token)
	}
	if r.read("secret") != string(token) {
		t.Error("Cloudflare did not get the token from the file")
	}
	if strings.Contains(out, strings.TrimSpace(string(token))) {
		t.Error("the token appeared in the output")
	}
	want := "cravv-connect setup --relay " + aliceURL + " --relay-token - < " + r.tokenFile()
	if !strings.Contains(out, want) {
		t.Errorf("output lacks the setup command %q:\n%s", want, out)
	}
	saved, _ := os.ReadFile(filepath.Join(r.relay, ".relay-url"))
	if string(saved) != aliceURL+"\n" {
		t.Errorf(".relay-url = %q", saved)
	}
}

// A second run (an update) redoes nothing that is already there and deploys
// once, with the pinned origin, keeping the token.
func TestRelayDeployAgainOnlyRedeploys(t *testing.T) {
	r := newDeployRig(t)
	if code, out, _ := r.run(); code != 0 {
		t.Fatalf("first run: exit %d\n%s", code, out)
	}
	token, _ := os.ReadFile(r.tokenFile())

	code, out, calls := r.run()
	if code != 0 {
		t.Fatalf("second run: exit %d\n%s", code, out)
	}
	for _, never := range []string{"npm ci", "wrangler login", "wrangler r2 bucket create", "wrangler r2 bucket lifecycle add"} {
		if count(calls, never) != 0 {
			t.Errorf("second run called %q", never)
		}
	}
	if count(calls, "wrangler deploy") != 1 || count(calls, "wrangler deploy --var PUBLIC_ORIGIN:"+aliceURL) != 1 {
		t.Errorf("want one pinned deploy, calls:\n%s", strings.Join(calls, "\n"))
	}
	if again, _ := os.ReadFile(r.tokenFile()); string(again) != string(token) {
		t.Error("the token changed")
	}
}

// Renaming the workers.dev subdomain changes the relay URL: the script pins
// the new one and says that machines must move.
func TestRelayDeployAfterSubdomainRename(t *testing.T) {
	r := newDeployRig(t)
	if code, out, _ := r.run(); code != 0 {
		t.Fatalf("first run: exit %d\n%s", code, out)
	}
	r.set("subdomain", "bob")
	code, out, calls := r.run()
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	bob := "https://cravv-relay.bob.workers.dev"
	if count(calls, "wrangler deploy --var PUBLIC_ORIGIN:"+bob) != 1 {
		t.Errorf("new URL not pinned, calls:\n%s", strings.Join(calls, "\n"))
	}
	if !strings.Contains(out, "cravv-connect setup --reset --relay "+bob) {
		t.Errorf("no advice to move machines:\n%s", out)
	}
	if !strings.Contains(out, "cravv-connect setup --relay "+bob) {
		t.Errorf("setup command not for the new URL:\n%s", out)
	}
}

// The account steps a script cannot do get a clear next step.
func TestRelayDeployExplainsWhatToDo(t *testing.T) {
	cases := []struct {
		name, flag string
		env        []string
		want       string
	}{
		{"old node", "", []string{"FAKE_NODE=20.11.0"}, "Node.js 22 or newer"},
		{"no R2", "no_r2", nil, "R2 Object Storage"},
		{"no subdomain", "no_subdomain", nil, "Workers & Pages"},
		{"not answering", "unhealthy", []string{"HEALTH_TRIES=1"}, "does not answer yet"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newDeployRig(t)
			if c.flag != "" {
				r.set(c.flag, "")
			}
			code, out, _ := r.run(c.env...)
			if code == 0 {
				t.Fatalf("exit 0, want a failure:\n%s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out)
			}
		})
	}
}
