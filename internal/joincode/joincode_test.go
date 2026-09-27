package joincode

import (
	"errors"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/bindcode"
)

var bind = bindcode.Code{Nameplate: "7K3F", Secret: "9QXMTR2A"}

func TestStringFormat(t *testing.T) {
	c, err := New("HTTPS://Relay.Example.com:443/", bind)
	if err != nil {
		t.Fatal(err)
	}
	if c.Relay != "https://relay.example.com" {
		t.Fatalf("relay %q, want the normalized origin", c.Relay)
	}
	if got, want := c.String(), "cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33n:7K3F-9QXMTR2A"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	c2, _ := New("http://mac.local:8787", bind)
	if got, want := c2.String(), "cravv-join:nb2hi4b2f4xw2yldfzwg6y3bnq5dqnzyg4:7K3F-9QXMTR2A"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestNewRefusesBadInput(t *testing.T) {
	if _, err := New("https://relay.example.com/v1", bind); err == nil {
		t.Error("relay with a path accepted")
	}
	if _, err := New("https://relay.example.com", bindcode.Code{}); err == nil {
		t.Error("zero bind code accepted")
	}
}

// Join codes are copied from terminals, chats and QR scanners: any case,
// spaces or line breaks anywhere, and the QR code carries it upper-cased.
func TestParseIsForgiving(t *testing.T) {
	c, _ := New("https://relay.example.com", bind)
	s := c.String()
	for _, in := range []string{
		s,
		strings.ToUpper(s),
		"  " + s + "\n",
		s[:20] + "\n  " + s[20:40] + " " + s[40:],
		strings.Replace(s, "7K3F-9QXMTR2A", "7k3f-9qxm-tr2a", 1),
		strings.Replace(s, "7K3F-9QXMTR2A", "7K3F9QXMTR2A", 1),
		strings.Replace(s, "cravv-join", "Cravv-Join", 1),
	} {
		got, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if got != c {
			t.Errorf("Parse(%q) = %+v, want %+v", in, got, c)
		}
		if !Is(in) {
			t.Errorf("Is(%q) = false", in)
		}
	}
}

func TestParseRefusesGarbage(t *testing.T) {
	c, _ := New("https://relay.example.com", bind)
	s := c.String()
	for _, in := range []string{
		"",
		"CRAVV-7K3F-9QXM-TR2A",
		"cravv-join:" + strings.SplitN(s, ":", 3)[1],
		"cravv-joins:" + strings.SplitN(s, ":", 2)[1],
		s + ":extra",
		strings.Replace(s, "nb2h", "nb2!", 1), // not base32
		"cravv-join:nb2hi4dthixs64tfnrqxsltfpbqw24dmmuxgg33nf53dc:7K3F-9QXMTR2A", // https://relay.example.com/v1
		"cravv-join:mz2haorpf54a:7K3F-9QXMTR2A",                                  // ftp://x
		strings.Replace(s, "7K3F-9QXMTR2A", "7K3F-9QXM", 1),                      // secret too short
		strings.Replace(s, "7K3F-9QXMTR2A", "7K3F-9QXMTR2U", 1),                  // U is not Crockford
		strings.Replace(s, "cravv", "cravvı", 1),                                 // non-ASCII
		s + strings.Repeat(" ", MaxLen),
	} {
		if _, err := Parse(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) = %v, want ErrInvalid", in, err)
		}
	}
}

func TestBindCode(t *testing.T) {
	c, _ := New("https://relay.example.com", bind)
	got, err := BindCode("cravv-7k3f-9qxm-tr2a", "https://relay.example.com")
	if err != nil || got != "cravv-7k3f-9qxm-tr2a" {
		t.Fatalf("plain bind code: %q %v, want it unchanged", got, err)
	}
	got, err = BindCode(c.String(), "HTTPS://relay.example.com:443")
	if err != nil || got != "CRAVV-7K3F-9QXM-TR2A" {
		t.Fatalf("join code: %q %v", got, err)
	}
	_, err = BindCode(c.String(), "https://other.example.com")
	if !errors.Is(err, ErrOtherRelay) || !strings.Contains(err.Error(), "https://relay.example.com") ||
		!strings.Contains(err.Error(), "https://other.example.com") {
		t.Fatalf("other relay: %v", err)
	}
	if _, err := BindCode(c.String(), ""); !errors.Is(err, ErrOtherRelay) {
		t.Fatalf("no relay configured: %v", err)
	}
	if _, err := BindCode("cravv-join:zz:7K3F-9QXMTR2A", "https://relay.example.com"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad join code: %v", err)
	}
}

// rawJoin builds a join code around any relay string, bypassing New, the way
// a hostile code would be made.
func rawJoin(relay string) string {
	return Prefix + ":" + strings.ToLower(b32.EncodeToString([]byte(relay))) + ":7K3F-9QXMTR2A"
}

func TestParseRefusesNonASCIIRelay(t *testing.T) {
	for _, relay := range []string{
		"https://\u0430pple.com",          // Cyrillic lookalike
		"https://relay.ex\u0430mple.com",  // Cyrillic a in the middle
		"https://relay\u200b.example.com", // zero-width space
		"https://\u202erelay.example.com", // bidi override
		"https://relay.example.com\u200f", // right-to-left mark
		"https://relay.example.com ",
		"https://relay.example.com\x1b[2J",
	} {
		if c, err := Parse(rawJoin(relay)); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(join code for %q) = %+v, %v; want ErrInvalid", relay, c, err)
		}
	}
	c, err := Parse(rawJoin("https://xn--pple-43d.com"))
	if err != nil || c.Relay != "https://xn--pple-43d.com" {
		t.Fatalf("punycode relay: %+v %v", c, err)
	}
}
