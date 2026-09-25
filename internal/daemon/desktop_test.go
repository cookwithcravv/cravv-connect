package daemon

import "testing"

func TestAppleScriptString(t *testing.T) {
	cases := map[string]string{
		"plain":            `"plain"`,
		`say "hi"`:         `"say \"hi\""`,
		`back\slash`:       `"back\\slash"`,
		"line\nbreak\x07!": `"line break !"`,
	}
	for in, want := range cases {
		if got := appleScriptString(in); got != want {
			t.Errorf("appleScriptString(%q) = %s, want %s", in, got, want)
		}
	}
}
