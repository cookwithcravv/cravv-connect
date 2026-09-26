package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func releaseWorkflow(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The release workflow builds the four platforms on native runners, the
// relay and conformance tools without cgo, and archives named the way
// scripts/install.sh expects.
func TestReleaseWorkflowShape(t *testing.T) {
	wf := releaseWorkflow(t)
	for _, want := range []string{
		"{ runner: macos-14, goos: darwin, goarch: arm64 }",
		"{ runner: macos-15-intel, goos: darwin, goarch: amd64 }",
		// Linux builds link glibc through cgo: the oldest runner keeps the
		// requirement at glibc 2.35 (Ubuntu 22.04, Debian 12).
		"{ runner: ubuntu-22.04, goos: linux, goarch: amd64 }",
		"{ runner: ubuntu-22.04-arm, goos: linux, goarch: arm64 }",
		`test "$(printf '%s\n' "$need" 2.35 | sort -V | tail -n 1)" = 2.35`,
		"libpam0g-dev",
		`CGO_ENABLED=1 go build`,
		`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$name/cravv-relay" ./cmd/cravv-relay`,
		`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "dist/$name/cravv-conformance" ./cmd/cravv-conformance`,
		`name="cravv-connect_${version}_${GOOS}_${GOARCH}"`,
		`version="${TAG#v}"`,
		`sha256sum *.tar.gz > SHA256SUMS`,
		`tags: ["v*"]`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml lacks %s", want)
		}
	}
}

// The workflow stamps the tag into the binary with -X; a wrong variable path
// would silently leave "dev". Build with the workflow's flag and ask.
func TestReleaseWorkflowStampsTheVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	m := regexp.MustCompile(`-X (\S+)=\$\{TAG\}`).FindStringSubmatch(releaseWorkflow(t))
	if m == nil {
		t.Fatal("release.yml does not stamp the version with -X <variable>=${TAG}")
	}
	bin := filepath.Join(t.TempDir(), "cravv-connect")
	build := exec.Command("go", "build", "-ldflags", "-X "+m[1]+"=v9.9.9-test", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "version").Output()
	if want := "cravv-connect v9.9.9-test (" + runtime.GOOS + "/" + runtime.GOARCH + ")\n"; err != nil || string(out) != want {
		t.Fatalf("version: %v %q, want %q", err, out, want)
	}
}

// The README states the glibc the Linux release binaries need.
func TestReadmeStatesGlibc(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "glibc 2.35 or newer") {
		t.Error("README does not state the minimum glibc")
	}
}

// The publish job attests the archives' build provenance (the README says how
// to verify it), with only the permissions that needs.
func TestReleaseWorkflowAttests(t *testing.T) {
	wf := releaseWorkflow(t)
	publish := wf[strings.Index(wf, "\n  publish:"):]
	for _, want := range []string{
		"      id-token: write\n",
		"      attestations: write\n",
		"      contents: write\n",
		"uses: actions/attest-build-provenance@",
		"subject-path: dist/*.tar.gz",
	} {
		if !strings.Contains(publish, want) {
			t.Errorf("publish job lacks %q", want)
		}
	}
	if strings.Contains(wf[:strings.Index(wf, "\n  publish:")], "id-token: write") {
		t.Error("the build job can mint OIDC tokens")
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "gh attestation verify cravv-connect_") {
		t.Error("README does not say how to verify the attestation")
	}
}

// Every action in the workflows is pinned to a full commit SHA with its
// version in a comment, and checkout never leaves the token in .git/config.
func TestWorkflowsPinActions(t *testing.T) {
	uses := regexp.MustCompile(`(?m)^\s*(?:-\s+)?uses:\s*(\S+)(.*)$`)
	pinned := regexp.MustCompile(`^[\w.-]+/[\w.-]+@[0-9a-f]{40}$`)
	version := regexp.MustCompile(`^\s+# v\d+\.\d+\.\d+$`)
	for _, name := range []string{"release.yml", "ci.yml"} {
		b, err := os.ReadFile("../../.github/workflows/" + name)
		if err != nil {
			t.Fatal(err)
		}
		wf := string(b)
		all := uses.FindAllStringSubmatch(wf, -1)
		if len(all) == 0 {
			t.Fatalf("%s uses no actions", name)
		}
		for _, m := range all {
			if !pinned.MatchString(m[1]) || !version.MatchString(m[2]) {
				t.Errorf("%s: %q is not pinned as owner/repo@<sha> # vX.Y.Z", name, strings.TrimSpace(m[0]))
			}
		}
		steps := strings.Split(wf, "\n      - ")
		checkouts := 0
		for _, step := range steps {
			if strings.HasPrefix(step, "uses: actions/checkout@") {
				checkouts++
				if !strings.Contains(step, "persist-credentials: false") {
					t.Errorf("%s: a checkout step keeps its credentials", name)
				}
			}
		}
		if checkouts == 0 && name == "ci.yml" {
			t.Errorf("%s: no checkout step found", name)
		}
	}
}

// CI checks the formatting of every Go file in the repository (conformance/
// and scripts/ too), not a list of folders that can fall behind.
func TestCIFormatsAllGoFiles(t *testing.T) {
	b, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	if want := `run: test -z "$(git ls-files -z '*.go' | xargs -0 gofmt -l)"`; !strings.Contains(string(b), want) {
		t.Errorf("ci.yml lacks %s", want)
	}
}
