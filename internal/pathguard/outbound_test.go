package pathguard

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

// writeFile creates dir/rel (and parents) with some content.
func writeFile(t *testing.T, dir, rel string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestOutboundAllowsRegularFilesInProject(t *testing.T) {
	proj := t.TempDir()
	writeFile(t, proj, "src/main.go")
	writeFile(t, proj, "results/model.ckpt")
	o := NewOutbound(nil)
	for _, p := range []string{"src/main.go", filepath.Join(proj, "results/model.ckpt"), "./src/../src/main.go"} {
		got, fi, err := o.Check(proj, p)
		if err != nil {
			t.Fatalf("Check(%q): %v", p, err)
		}
		if !filepath.IsAbs(got) || fi == nil || fi.Size() != 4 {
			t.Fatalf("Check(%q) = %q, %v", p, got, fi)
		}
	}
	got, _, _ := o.Check(proj, "src/main.go")
	if got != realPath(t, filepath.Join(proj, "src/main.go")) {
		t.Fatalf("returned %q, want the resolved real path", got)
	}
}

func TestOutboundRefusesHostilePaths(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	outside := filepath.Join(base, "outside")
	writeFile(t, proj, "ok.txt")
	writeFile(t, outside, "secret.txt")
	writeFile(t, proj, ".git/config")
	writeFile(t, proj, ".ssh/known_hosts")
	writeFile(t, proj, "sub/.hidden/notes.txt")
	writeFile(t, proj, ".env")
	writeFile(t, proj, ".env.local")
	writeFile(t, proj, "certs/key.pem")
	writeFile(t, proj, "certs/SERVER.KEY")
	writeFile(t, proj, "id_ed25519")
	writeFile(t, proj, "deploy/server.key")
	if err := os.MkdirAll(filepath.Join(proj, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Symlinks: a file link out of the project, a dir link out of the
	// project, and a link inside the project pointing into a dot-dir.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(proj, "escape.txt")))
	must(os.Symlink(outside, filepath.Join(proj, "outdir")))
	must(os.Symlink(filepath.Join(proj, ".git/config"), filepath.Join(proj, "innocent.txt")))
	must(os.Symlink(filepath.Join(proj, "ok.txt"), filepath.Join(proj, ".sneaky")))
	must(syscall.Mkfifo(filepath.Join(proj, "pipe"), 0o644))
	big := filepath.Join(proj, "huge.bin")
	f, err := os.Create(big)
	must(err)
	must(f.Truncate(core.MaxFileBytes + 1)) // sparse, no real disk use
	must(f.Close())

	cases := []string{
		"../outside/secret.txt",
		filepath.Join(outside, "secret.txt"),
		"sub/../../outside/secret.txt",
		"escape.txt",
		"outdir/secret.txt",
		"innocent.txt",
		".sneaky",
		".git/config",
		".ssh/known_hosts",
		"sub/.hidden/notes.txt",
		".env",
		".env.local",
		"certs/key.pem",
		"certs/SERVER.KEY",
		"id_ed25519",
		"deploy/server.key",
		"adir",
		".",
		"",
		"pipe",
		"huge.bin",
		"does-not-exist.txt",
		"/etc/passwd",
	}
	o := NewOutbound(nil)
	for _, p := range cases {
		got, _, err := o.Check(proj, p)
		if !errors.Is(err, core.ErrPathRefused) {
			t.Errorf("Check(%q) = %q, %v; want ErrPathRefused", p, got, err)
		}
	}
	// Exactly at the limit is fine.
	must(os.Truncate(big, core.MaxFileBytes))
	if _, _, err := o.Check(proj, "huge.bin"); err != nil {
		t.Fatalf("file of exactly MaxFileBytes refused: %v", err)
	}
}

func TestOutboundExtraRoots(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	data := filepath.Join(base, "datasets")
	other := filepath.Join(base, "other")
	writeFile(t, proj, "a.txt")
	writeFile(t, data, "train.csv")
	writeFile(t, data, ".cache/x")
	writeFile(t, other, "b.txt")

	o := NewOutbound([]string{data})
	if _, _, err := o.Check(proj, filepath.Join(data, "train.csv")); err != nil {
		t.Fatalf("file in allowed root refused: %v", err)
	}
	if _, _, err := o.Check(proj, "../datasets/train.csv"); err != nil {
		t.Fatalf("relative path into allowed root refused: %v", err)
	}
	for _, p := range []string{filepath.Join(data, ".cache/x"), filepath.Join(other, "b.txt")} {
		if _, _, err := o.Check(proj, p); !errors.Is(err, core.ErrPathRefused) {
			t.Errorf("Check(%q) err = %v, want ErrPathRefused", p, err)
		}
	}
	// A symlink in the project into an allowed root is fine.
	if err := os.Symlink(filepath.Join(data, "train.csv"), filepath.Join(proj, "train-link.csv")); err != nil {
		t.Fatal(err)
	}
	got, _, err := o.Check(proj, "train-link.csv")
	if err != nil || got != realPath(t, filepath.Join(data, "train.csv")) {
		t.Fatalf("link into allowed root = %q, %v", got, err)
	}
	// Without the extra root the same file is refused.
	if _, _, err := NewOutbound(nil).Check(proj, filepath.Join(data, "train.csv")); !errors.Is(err, core.ErrPathRefused) {
		t.Fatalf("no extra root: err = %v", err)
	}
}

func TestOutboundCopiesRoots(t *testing.T) {
	roots := []string{t.TempDir()}
	o := NewOutbound(roots)
	roots[0] = "/"
	if _, _, err := o.Check(t.TempDir(), "/etc/hosts"); !errors.Is(err, core.ErrPathRefused) {
		t.Fatalf("caller mutation of roots slice widened access: %v", err)
	}
}

func TestOutboundRefusesMoreSecretFiles(t *testing.T) {
	proj := t.TempDir()
	secrets := []string{
		"prod.env", "config/Staging.ENV",
		"certs/client.p12", "certs/client.PFX",
		"java/app.jks", "java/release.keystore",
		"vault/passwords.kdbx",
		"putty/server.ppk",
		"credentials.json", "gcp/credentials-prod.json", "Credentials.JSON",
		"service-account.json", "gcp/service-account-ci.json",
		"vpn/office.ovpn",
		".netrc", ".npmrc", ".pypirc",
		"id_rsa", "keys/id_ecdsa.pub",
	}
	for _, s := range secrets {
		writeFile(t, proj, s)
	}
	o := NewOutbound(nil)
	for _, s := range secrets {
		if _, _, err := o.Check(proj, s); !errors.Is(err, core.ErrPathRefused) {
			t.Errorf("Check(%q) err = %v, want ErrPathRefused", s, err)
		}
	}
	// Near misses stay allowed.
	fine := []string{"environment.md", "envelope.go", "credentials.md", "my-credentials.json", "service-accounts.md", "keystore.go", "p12.txt"}
	for _, s := range fine {
		writeFile(t, proj, s)
		if _, _, err := o.Check(proj, s); err != nil {
			t.Errorf("Check(%q) refused an ordinary file: %v", s, err)
		}
	}
}
