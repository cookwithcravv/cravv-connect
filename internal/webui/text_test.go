package webui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/present"
)

func TestPeerTextUnwrapsAndCleans(t *testing.T) {
	body := "purpose: <b>train</b> & ‮evil‬\nnote: literal &lt; stays\tX\x1b[2J​\U000E0041"
	wrapped := present.Wrap(present.Item{Alias: "gpu-box", Session: "trainer", ID: "L1", Kind: "link", Body: body})
	got := peerText(wrapped)
	want := "purpose: <b>train</b> & evil\nnote: literal &lt; stays    X[2J"
	if got != want {
		t.Fatalf("peerText = %q\nwant       %q", got, want)
	}
	if peerText("") != "" {
		t.Fatal("empty wrapper")
	}
	if got := peerText("plain\x07 text"); got != "plain text" {
		t.Fatalf("unwrapped text: %q", got)
	}
}

func TestCleanLineRemovesHidingCharacters(t *testing.T) {
	in := "a\nb\rc\td\x1b​e⁦f g\U000E0067h\xff"
	if got := cleanLine(in); got != "abcdefgh�" {
		t.Fatalf("cleanLine = %q", got)
	}
}

// User-facing copy (templates, scripts, styles and the Go strings of this
// package) has no em dashes.
func TestUICopyHasNoEmDashes(t *testing.T) {
	fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(assets, p)
		if strings.ContainsRune(string(b), 0x2014) {
			t.Errorf("%s has an em dash", p)
		}
		return nil
	})
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsRune(string(b), 0x2014) {
			t.Errorf("%s has an em dash", f)
		}
	}
}
