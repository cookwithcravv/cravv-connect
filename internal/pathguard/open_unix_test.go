//go:build unix

package pathguard

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestOutboundOpenReturnsCheckedFile(t *testing.T) {
	proj := t.TempDir()
	writeFile(t, proj, "src/main.go")
	f, fi, err := NewOutbound(nil).Open(proj, "src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if fi.Size() != 4 || !fi.Mode().IsRegular() {
		t.Fatalf("info = %v", fi)
	}
	b, err := io.ReadAll(io.LimitReader(f, fi.Size()))
	if err != nil || string(b) != "data" {
		t.Fatalf("read %q, %v", b, err)
	}
	if f.Name() != realPath(t, filepath.Join(proj, "src/main.go")) {
		t.Fatalf("opened %q", f.Name())
	}
}

func TestOutboundOpenRefusesWhatCheckRefuses(t *testing.T) {
	proj := t.TempDir()
	writeFile(t, proj, ".env")
	for _, p := range []string{".env", "missing.txt", "../x"} {
		f, _, err := NewOutbound(nil).Open(proj, p)
		if !errors.Is(err, core.ErrPathRefused) || f != nil {
			t.Fatalf("Open(%q) = %v, %v; want ErrPathRefused and no file", p, f, err)
		}
	}
}

// A hard link has no path relationship to its other names, so a link in the
// project could expose a secret stored anywhere on the same filesystem.
func TestOutboundOpenRefusesHardLinks(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "proj")
	secret := writeFile(t, base, "outside/secret.txt")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(proj, "innocent.txt")); err != nil {
		t.Fatal(err)
	}
	f, _, err := NewOutbound(nil).Open(proj, "innocent.txt")
	if !errors.Is(err, core.ErrPathRefused) || f != nil {
		t.Fatalf("Open(hard link) = %v, %v; want ErrPathRefused", f, err)
	}
}

// swapAfterCheck replaces the checked file between Check and open.
func swapAfterCheck(t *testing.T, swap func(real string)) {
	t.Helper()
	afterCheckHook = swap
	t.Cleanup(func() { afterCheckHook = nil })
}

func TestOutboundOpenDetectsSwapAfterCheck(t *testing.T) {
	cases := map[string]func(t *testing.T, outside string) func(real string){
		"replaced by another file": func(t *testing.T, outside string) func(string) {
			return func(real string) {
				tmp := real + ".new"
				if err := os.WriteFile(tmp, []byte("other"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, real); err != nil {
					t.Fatal(err)
				}
			}
		},
		"replaced by a symlink": func(t *testing.T, outside string) func(string) {
			return func(real string) {
				os.Remove(real)
				if err := os.Symlink(outside, real); err != nil {
					t.Fatal(err)
				}
			}
		},
		"replaced by a fifo": func(t *testing.T, outside string) func(string) {
			return func(real string) {
				os.Remove(real)
				if err := syscall.Mkfifo(real, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			proj := filepath.Join(base, "proj")
			writeFile(t, proj, "a.txt")
			outside := writeFile(t, base, "outside/secret.txt")
			swapAfterCheck(t, mk(t, outside))
			done := make(chan struct{})
			var (
				f   *os.File
				err error
			)
			go func() {
				defer close(done)
				f, _, err = NewOutbound(nil).Open(proj, "a.txt")
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Open hung")
			}
			if f != nil {
				f.Close()
			}
			if !errors.Is(err, core.ErrPathRefused) || f != nil {
				t.Fatalf("Open after swap = %v, %v; want ErrPathRefused", f, err)
			}
		})
	}
}
