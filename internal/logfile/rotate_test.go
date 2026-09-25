package logfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRotatesAtSizeAndKeepsN(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.log")
	w, err := Open(p, 10, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n", "eeee\n", "ffff\n", "gggg\n"} {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "gggg\n" {
		t.Fatalf("current = %q", got)
	}
	if got := read(t, p+".1"); got != "eeee\nffff\n" {
		t.Fatalf(".1 = %q", got)
	}
	if got := read(t, p+".2"); got != "cccc\ndddd\n" {
		t.Fatalf(".2 = %q", got)
	}
	if _, err := os.Stat(p + ".3"); !os.IsNotExist(err) {
		t.Fatalf("kept more than 2 old files: %v", err)
	}
}

func TestReopenAppendsAndCountsExistingSize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.log")
	os.WriteFile(p, []byte("12345678\n"), 0o600)
	w, err := Open(p, 10, 3)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("next\n"))
	w.Close()
	if got := read(t, p+".1"); got != "12345678\n" {
		t.Fatalf(".1 = %q", got)
	}
	if got := read(t, p); got != "next\n" {
		t.Fatalf("current = %q", got)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestOversizeWriteGoesToAFreshFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "daemon.log")
	w, _ := Open(p, 4, 1)
	w.Write([]byte("ab\n"))
	big := strings.Repeat("x", 20) + "\n"
	if n, err := w.Write([]byte(big)); err != nil || n != len(big) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	w.Close()
	if read(t, p) != big || read(t, p+".1") != "ab\n" {
		t.Fatalf("current %q, .1 %q", read(t, p), read(t, p+".1"))
	}
}
