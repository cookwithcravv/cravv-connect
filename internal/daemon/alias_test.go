package daemon

import "testing"

func TestSanitizeAlias(t *testing.T) {
	cases := map[string]string{
		"gpu-box":                               "gpu-box",
		"Alice's MacBook Pro":                   "alice-s-macbook-pro",
		"  --weird__name!!  ":                   "weird-name",
		"ÜBER box":                              "ber-box",
		"":                                      "",
		"!!!":                                   "",
		"a-very-long-machine-name-that-goes-on": "a-very-long-machine-name",
		"abcdefghijklmnopqrstuvw-xyz":           "abcdefghijklmnopqrstuvw",
	}
	for in, want := range cases {
		if got := SanitizeAlias(in); got != want {
			t.Errorf("SanitizeAlias(%q) = %q, want %q", in, got, want)
		}
		if got := SanitizeAlias(in); len(got) > MaxAliasLen {
			t.Errorf("SanitizeAlias(%q) longer than %d", in, MaxAliasLen)
		}
	}
}
