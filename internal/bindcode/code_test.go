package bindcode

import (
	"errors"
	"regexp"
	"testing"
)

var codePattern = regexp.MustCompile(`^CRAVV-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

func TestNewCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	for range 500 {
		c, err := NewCode("7K3F")
		if err != nil {
			t.Fatal(err)
		}
		s := c.String()
		if !codePattern.MatchString(s) {
			t.Fatalf("code %q does not match format", s)
		}
		if c.Nameplate != "7K3F" || len(c.Secret) != SecretLen {
			t.Fatalf("unexpected code %+v", c)
		}
		seen[c.Secret] = true
	}
	if len(seen) < 490 {
		t.Fatalf("only %d distinct secrets in 500 codes", len(seen))
	}
}

func TestNewCodeNormalizesNameplate(t *testing.T) {
	c, err := NewCode("7k3o")
	if err != nil {
		t.Fatal(err)
	}
	if c.Nameplate != "7K30" {
		t.Fatalf("nameplate = %q, want 7K30", c.Nameplate)
	}
	for _, bad := range []string{"", "7K3", "7K3FF", "7KU3", "7K-3"} {
		if _, err := NewCode(bad); err == nil {
			t.Errorf("NewCode(%q) accepted", bad)
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	c, _ := NewCode("ABCD")
	back, err := Parse(c.String())
	if err != nil {
		t.Fatal(err)
	}
	if back != c {
		t.Fatalf("Parse(String()) = %+v, want %+v", back, c)
	}
}

func TestParseForgiving(t *testing.T) {
	want := Code{Nameplate: "7K3F", Secret: "9QXMTR2A"}
	inputs := []string{
		"CRAVV-7K3F-9QXM-TR2A",
		"cravv-7k3f-9qxm-tr2a",
		"  CRAVV-7K3F-9QXM-TR2A\n",
		"CRAVV 7K3F 9QXM TR2A",
		"cravv7k3f9qxmtr2a",
		"CRAVV-7K3F-9QXMTR2A",
		"Cravv - 7K3F - 9QXM - TR2A",
	}
	for _, in := range inputs {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Parse(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestParseConfusableLetters(t *testing.T) {
	tests := []struct {
		in   string
		want Code
	}{
		{"CRAVV-O0O0-IiLl-1111", Code{Nameplate: "0000", Secret: "11111111"}},
		{"cravv-10ab-oilx-yz99", Code{Nameplate: "10AB", Secret: "011XYZ99"}},
	}
	for _, tt := range tests {
		got, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	inputs := []string{
		"",
		"CRAVV",
		"7K3F-9QXM-TR2A",          // no prefix
		"CRAV-7K3F-9QXM-TR2A",     // wrong prefix
		"XRAVV-7K3F-9QXM-TR2A",    // wrong prefix
		"CRAVV-7K3F-9QXM-TR2",     // too short
		"CRAVV-7K3F-9QXM-TR2AB",   // too long
		"CRAVV-7K3F-9QXM-TR2U",    // U excluded
		"CRAVV-7K3F-9QXM-TR2*",    // invalid char
		"CRAVV-7K3F_9QXM-TR2A",    // underscore
		"CRAVV-7K3F-9QXM-TR2A-00", // extra group
	}
	for _, in := range inputs {
		if _, err := Parse(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", in, err)
		}
	}
}
