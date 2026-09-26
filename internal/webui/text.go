package webui

import (
	"strings"
	"unicode"

	"github.com/cravv/cravv-connect/internal/present"
)

// cleanLine removes what could hide or reorder text on the page: control
// characters (newlines included), Unicode format characters (bidi controls,
// zero-width characters), line and paragraph separators, and the invisible
// characters present.Wrap strips. It is the CLI's terminal cleaning;
// html/template then escapes the result.
func cleanLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || present.IsInvisible(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�"))
}

// xmlUnescaper undoes present.Wrap's body escaping; html/template escapes
// the text again for the page.
var xmlUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

// peerText turns a <remote_message> wrapper (a view's Wrapped field) into
// plain text for the page: the wrapper lines are dropped (the page names the
// machine and session from validated fields), the body's escaping is undone
// and every line is cleaned. Tabs become four spaces.
func peerText(wrapped string) string {
	if wrapped == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(wrapped, "\r\n", "\n"), "\n")
	if n := len(lines); n >= 2 && strings.HasPrefix(lines[0], "<remote_message") && lines[n-1] == "</remote_message>" {
		lines = lines[1 : n-1]
	}
	for i, l := range lines {
		lines[i] = cleanLine(strings.ReplaceAll(xmlUnescaper.Replace(l), "\t", "    "))
	}
	return strings.Join(lines, "\n")
}
