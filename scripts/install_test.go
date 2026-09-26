// Package scripts holds the tests of scripts/install.sh: a fake release
// server and the script run with sh (and dash when installed).
package scripts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// fakeRelease serves <base>/latest (a redirect to <base>/tag/<latest>) and
// <base>/download/<tag>/<file> for each tag, like GitHub releases.
type fakeRelease struct {
	latest string
	mu     sync.Mutex
	files  map[string][]byte // "<tag>/<file>"
	srv    *httptest.Server
}

func (r *fakeRelease) file(k string) ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.files[k]
	return b, ok
}

func (r *fakeRelease) set(k string, b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.files[k] = b
}

func archiveName(tag string) string {
	return fmt.Sprintf("cravv-connect_%s_%s_%s", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

// tarball builds the release archive: a directory named like the archive
// holding fake binaries that print their name and tag.
func tarball(t *testing.T, tag string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dir := archiveName(tag)
	tw.WriteHeader(&tar.Header{Name: dir + "/", Typeflag: tar.TypeDir, Mode: 0o755})
	for _, b := range []string{"cravv-connect", "cravv-relay", "cravv-conformance", "README.md"} {
		body := []byte("#!/bin/sh\necho " + b + " " + tag + "\n")
		if err := tw.WriteHeader(&tar.Header{Name: dir + "/" + b, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write(body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	gz.Close()
	return buf.Bytes()
}

func newFakeRelease(t *testing.T, latest string, tags ...string) *fakeRelease {
	r := &fakeRelease{latest: latest, files: map[string][]byte{}}
	for _, tag := range tags {
		tgz := tarball(t, tag)
		sum := sha256.Sum256(tgz)
		name := archiveName(tag) + ".tar.gz"
		r.files[tag+"/"+name] = tgz
		r.files[tag+"/SHA256SUMS"] = []byte(fmt.Sprintf("%s  %s\n%s  cravv-connect_other_linux_s390x.tar.gz\n", hex.EncodeToString(sum[:]), name, strings.Repeat("0", 64)))
	}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch p := req.URL.Path; {
		case p == "/releases/latest":
			http.Redirect(w, req, "/releases/tag/"+r.latest, http.StatusFound)
		case strings.HasPrefix(p, "/releases/tag/"):
			w.Write([]byte("<html>release</html>"))
		case strings.HasPrefix(p, "/releases/download/"):
			b, ok := r.file(strings.TrimPrefix(p, "/releases/download/"))
			if !ok {
				http.NotFound(w, req)
				return
			}
			w.Write(b)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// shells are the POSIX shells the script is run with.
func shells(t *testing.T) []string {
	out := []string{"sh"}
	if _, err := exec.LookPath("dash"); err == nil {
		out = append(out, "dash")
	}
	return out
}

type result struct {
	code           int
	stdout, stderr string
	home           string
}

// install runs scripts/install.sh with shell in a fresh HOME against r.
func install(t *testing.T, shell string, r *fakeRelease, env []string, args ...string) result {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command(shell, append([]string{"install.sh"}, args...)...)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "CRAVV_BASE_URL=" + r.srv.URL + "/releases"}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{code, out.String(), errb.String(), home}
}

func ran(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command(bin).Output()
	if err != nil {
		t.Fatalf("%s: %v", bin, err)
	}
	return string(out)
}

// With no version the latest release is installed to ~/.local/bin, both
// binaries executable, with a hint when that folder is not on PATH.
func TestInstallLatest(t *testing.T) {
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	for _, sh := range shells(t) {
		res := install(t, sh, r, nil)
		if res.code != 0 {
			t.Fatalf("%s: code %d stderr %s", sh, res.code, res.stderr)
		}
		bin := filepath.Join(res.home, ".local", "bin")
		if got := ran(t, filepath.Join(bin, "cravv-connect")); got != "cravv-connect v1.2.3\n" {
			t.Fatalf("%s: cravv-connect says %q", sh, got)
		}
		if got := ran(t, filepath.Join(bin, "cravv-relay")); got != "cravv-relay v1.2.3\n" {
			t.Fatalf("%s: cravv-relay says %q", sh, got)
		}
		if _, err := os.Stat(filepath.Join(bin, "cravv-conformance")); err == nil {
			t.Fatalf("%s: installed the conformance tool", sh)
		}
		for _, want := range []string{
			"Downloading cravv-connect v1.2.3 for " + runtime.GOOS + "/" + runtime.GOARCH + "...\n",
			"Installed cravv-connect v1.2.3 and cravv-relay to " + bin + ".\n",
			bin + " is not on your PATH.",
			"Next, on the first machine:  cravv-connect setup\n",
		} {
			if !strings.Contains(res.stdout, want) {
				t.Errorf("%s: stdout lacks %q:\n%s", sh, want, res.stdout)
			}
		}
	}
}

// CRAVV_VERSION pins a release; installing over an existing binary says to
// restart the daemon; a folder on PATH gets no hint.
func TestInstallPinnedVersionOverAnOldOne(t *testing.T) {
	r := newFakeRelease(t, "v1.2.3", "v1.2.3", "v1.0.0")
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "cravv-connect"), []byte("old"), 0o755)
	cmd := exec.Command("sh", "install.sh")
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":" + os.Getenv("PATH"), "CRAVV_BASE_URL=" + r.srv.URL + "/releases", "CRAVV_VERSION=v1.0.0"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := ran(t, filepath.Join(bin, "cravv-connect")); got != "cravv-connect v1.0.0\n" {
		t.Fatalf("cravv-connect says %q", got)
	}
	if strings.Contains(string(out), "is not on your PATH") || !strings.Contains(string(out), "cravv-connect daemon stop && cravv-connect daemon start") {
		t.Fatalf("output\n%s", out)
	}
}

// A download that does not match SHA256SUMS, or is missing from it, installs
// nothing.
func TestInstallChecksTheDownload(t *testing.T) {
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	name := archiveName("v1.2.3") + ".tar.gz"
	good, _ := r.file("v1.2.3/" + name)
	r.set("v1.2.3/"+name, append(append([]byte(nil), good...), 0))
	res := install(t, "sh", r, nil)
	if res.code == 0 || !strings.Contains(res.stderr, "install.sh: checksum mismatch for "+name) {
		t.Fatalf("tampered: code %d stderr %q", res.code, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(res.home, ".local", "bin", "cravv-connect")); err == nil {
		t.Fatal("installed a tampered archive")
	}
	r.set("v1.2.3/"+name, good)
	r.set("v1.2.3/SHA256SUMS", []byte(strings.Repeat("0", 64)+"  cravv-connect_other_linux_s390x.tar.gz\n"))
	if res := install(t, "sh", r, nil); res.code == 0 || !strings.Contains(res.stderr, "install.sh: SHA256SUMS has no entry for "+name) {
		t.Fatalf("no entry: code %d stderr %q", res.code, res.stderr)
	}
}

func TestInstallBadInput(t *testing.T) {
	r := newFakeRelease(t, "latest-junk", "v1.2.3")
	if res := install(t, "sh", r, nil); res.code == 0 || !strings.Contains(res.stderr, "not a release tag: latest-junk") {
		t.Fatalf("bad latest tag: code %d stderr %q", res.code, res.stderr)
	}
	if res := install(t, "sh", r, []string{"CRAVV_VERSION=v1;rm"}); res.code == 0 || !strings.Contains(res.stderr, "not a release tag: v1;rm") {
		t.Fatalf("bad pinned tag: code %d stderr %q", res.code, res.stderr)
	}
	if res := install(t, "sh", r, []string{"CRAVV_VERSION=v9.9.9"}); res.code == 0 || !strings.Contains(res.stderr, "download failed: "+r.srv.URL+"/releases/download/v9.9.9/") {
		t.Fatalf("missing release: code %d stderr %q", res.code, res.stderr)
	}
	if res := install(t, "sh", r, nil, "--bogus"); res.code != 2 || !strings.Contains(res.stderr, "usage: install.sh [--system]") {
		t.Fatalf("bad option: code %d stderr %q", res.code, res.stderr)
	}
}

// --system installs to /usr/local/bin (here a stand-in folder the user cannot
// write) through sudo.
func TestInstallSystemUsesSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	sys := t.TempDir()
	os.Chmod(sys, 0o555)
	t.Cleanup(func() { os.Chmod(sys, 0o755) })
	fake := t.TempDir()
	sudoLog := filepath.Join(fake, "sudo.log")
	os.WriteFile(filepath.Join(fake, "sudo"), []byte("#!/bin/sh\necho \"$*\" >> "+sudoLog+"\nchmod u+w "+sys+"\nexec \"$@\"\n"), 0o755)
	res := install(t, "sh", r, []string{"CRAVV_SYSTEM_DIR=" + sys, "PATH=" + fake + ":" + os.Getenv("PATH")}, "--system")
	if res.code != 0 {
		t.Fatalf("code %d stderr %s", res.code, res.stderr)
	}
	if got := ran(t, filepath.Join(sys, "cravv-connect")); got != "cravv-connect v1.2.3\n" {
		t.Fatalf("cravv-connect says %q", got)
	}
	log, _ := os.ReadFile(sudoLog)
	if !strings.Contains(string(log), "mv -f "+sys+"/.cravv-connect.new "+sys+"/cravv-connect") || !strings.Contains(res.stdout, "Installing to "+sys+" needs sudo.") {
		t.Fatalf("sudo log %q stdout %s", log, res.stdout)
	}
	if _, err := os.Stat(filepath.Join(res.home, ".local", "bin")); err == nil {
		t.Fatal("--system also installed to ~/.local/bin")
	}
}

// Without CRAVV_BASE_URL the script asks GitHub for CRAVV_REPO's latest
// release (default cravv/cravv-connect). A fake curl records the request.
func TestInstallDefaultRepository(t *testing.T) {
	fake := t.TempDir()
	log := filepath.Join(fake, "curl.log")
	os.WriteFile(filepath.Join(fake, "curl"), []byte("#!/bin/sh\necho \"$*\" >> "+log+"\nexit 22\n"), 0o755)
	for repo, want := range map[string]string{"": "https://github.com/cravv/cravv-connect/releases/latest", "acme/cc": "https://github.com/acme/cc/releases/latest"} {
		os.Remove(log)
		cmd := exec.Command("sh", "install.sh")
		cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + fake + ":" + os.Getenv("PATH")}
		if repo != "" {
			cmd.Env = append(cmd.Env, "CRAVV_REPO="+repo)
		}
		out, err := cmd.CombinedOutput()
		got, _ := os.ReadFile(log)
		if err == nil || !strings.Contains(string(got), want) || !strings.Contains(string(out), "cannot find the latest release at "+strings.TrimSuffix(want, "/latest")+"/latest") {
			t.Fatalf("repo %q: %v\n%s\ncurl: %s", repo, err, out, got)
		}
	}
}

// Without curl the script uses wget (for the latest release too). PATH holds
// only the tools the script needs, minus curl.
func TestInstallWithWget(t *testing.T) {
	if _, err := exec.LookPath("wget"); err != nil {
		t.Skip("wget is not installed")
	}
	tools := t.TempDir()
	for _, tool := range []string{"wget", "uname", "sed", "awk", "tar", "gzip", "mktemp", "rm", "cp", "chmod", "mv", "mkdir", "tr", "tail", "sha256sum", "shasum", "perl", "sysctl", "cat"} {
		if p, err := exec.LookPath(tool); err == nil {
			os.Symlink(p, filepath.Join(tools, tool))
		}
	}
	r := newFakeRelease(t, "v1.2.3", "v1.2.3")
	res := install(t, "sh", r, []string{"PATH=" + tools})
	if res.code != 0 {
		t.Fatalf("code %d stderr %s", res.code, res.stderr)
	}
	if got := ran(t, filepath.Join(res.home, ".local", "bin", "cravv-connect")); got != "cravv-connect v1.2.3\n" {
		t.Fatalf("cravv-connect says %q", got)
	}
}
