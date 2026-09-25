package pathguard

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cravv/cravv-connect/internal/core"
)

func TestSanitizeHostileNames(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := []struct {
		in, want string
	}{
		{"report.pdf", "report.pdf"},
		{"../../.ssh/authorized_keys", "authorized_keys"},
		{`..\..\Windows\system32\evil.dll`, "evil.dll"},
		{".bashrc", "bashrc"},
		{"...hidden", "hidden"},
		{"..", "file"},
		{".", "file"},
		{"", "file"},
		{"/", "file"},
		{"dir/", "file"},
		{"héllo wörld.txt", "h_llo_w_rld.txt"},
		{"日本.txt", "__.txt"},
		{"a\x00b.txt", "a_b.txt"},
		{"line\nbreak\r.txt", "line_break_.txt"},
		{"tab\there", "tab_here"},
		{"\x1b[31mred", "__31mred"},
		{"$(rm -rf ~).sh", "__rm_-rf___.sh"},
		{"name‮txt.exe", "name_txt.exe"},
		{long, strings.Repeat("a", 100)},
		{long + ".tar.gz", strings.Repeat("a", 97) + ".gz"},
	}
	for _, c := range cases {
		got := SanitizeName(c.in)
		if got != c.want {
			t.Errorf("SanitizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Properties that must hold for every input.
	inputs := []string{long, "../../.ssh/authorized_keys", ".bashrc", "..", "", "\x00", ". . .", "-", "~/.profile"}
	for _, c := range cases {
		inputs = append(inputs, c.in)
	}
	for _, in := range inputs {
		got := SanitizeName(in)
		if got == "" || len(got) > MaxNameLen || strings.HasPrefix(got, ".") ||
			strings.ContainsAny(got, `/\`) || got == ".." {
			t.Errorf("SanitizeName(%q) = %q violates the name rules", in, got)
		}
		for i := 0; i < len(got); i++ {
			c := got[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
				t.Errorf("SanitizeName(%q) = %q contains byte %q", in, got, c)
			}
		}
	}
}

func TestInboundPathStaysInsideFilesDir(t *testing.T) {
	files := filepath.Join(t.TempDir(), "files")
	names := []string{"../../.ssh/authorized_keys", ".bashrc", strings.Repeat("x", 300), "..", "", "a\x00b", "ok.txt"}
	for _, n := range names {
		p, err := InboundPath(files, "gpu-box", "01J9ZABCDEFGHJKMNPQRSTVWXY", n)
		if err != nil {
			t.Fatalf("InboundPath(%q): %v", n, err)
		}
		rel, err := filepath.Rel(files, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("InboundPath(%q) = %q escapes %q", n, p, files)
		}
		dir, base := filepath.Split(rel)
		if filepath.Clean(dir) != "gpu-box" {
			t.Fatalf("InboundPath(%q) = %q not in the alias folder", n, p)
		}
		if !strings.HasPrefix(base, "01J9ZABCDEFGHJKMNPQRSTVWXY-") || strings.HasPrefix(base, ".") {
			t.Fatalf("InboundPath(%q) base = %q", n, base)
		}
	}
	want := filepath.Join(files, "gpu-box", "01J9ZABCDEFGHJKMNPQRSTVWXY-authorized_keys")
	if got, _ := InboundPath(files, "gpu-box", "01J9ZABCDEFGHJKMNPQRSTVWXY", "../../.ssh/authorized_keys"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInboundPathSameNameDifferentMessagesDiffer(t *testing.T) {
	files := t.TempDir()
	a, err1 := InboundPath(files, "laptop", "01J9ZAAAAAAAAAAAAAAAAAAAAA", "notes.txt")
	b, err2 := InboundPath(files, "laptop", "01J9ZBBBBBBBBBBBBBBBBBBBBB", "notes.txt")
	if err1 != nil || err2 != nil || a == b {
		t.Fatalf("reused name mapped to %q and %q (%v, %v)", a, b, err1, err2)
	}
}

func TestInboundPathRejectsBadComponents(t *testing.T) {
	files := t.TempDir()
	cases := []struct{ alias, msgID string }{
		{"..", "01J9Z"},
		{"../x", "01J9Z"},
		{".hidden", "01J9Z"},
		{"", "01J9Z"},
		{"a/b", "01J9Z"},
		{"gpu-box", ""},
		{"gpu-box", "../../etc"},
		{"gpu-box", "a/b"},
		{"gpu-box", ".x"},
		{"gpu-box", "x.y"},
		{"gpu-box", strings.Repeat("A", 65)},
		{"gpu-box", "id\x00"},
	}
	for _, c := range cases {
		if p, err := InboundPath(files, c.alias, c.msgID, "f.txt"); !errors.Is(err, core.ErrPathRefused) {
			t.Errorf("InboundPath(alias=%q, msgID=%q) = %q, %v; want ErrPathRefused", c.alias, c.msgID, p, err)
		}
	}
}
