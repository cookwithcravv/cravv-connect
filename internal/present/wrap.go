// Package present formats peer content for agents: the <remote_message>
// wrapper, the one-line hook notice, and the MCP standing instructions.
package present

import (
	"strings"
	"unicode"
)

// Item is one inbox item to show an agent. Alias is the local alias; every
// other string may be chosen by the peer.
type Item struct {
	Alias, Session, Trust, ID, Kind, TaskID string
	Body                                    string
}

// MaxAttrRunes caps every attribute value.
const MaxAttrRunes = 64

var (
	attrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")
	bodyEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
)

// Wrap renders it as
//
//	<remote_message from="..." session="..." trust="..." id="..." kind="..." task_id="...">
//	ESCAPED BODY
//	</remote_message>
//
// session and task_id are omitted when empty. Attribute values have control
// and invisible formatting characters removed, are capped at MaxAttrRunes
// runes and XML-escaped. The body has invisible characters removed (see
// isInvisible) and is XML-escaped (&, <, >) so it cannot close the tag or
// open a new one.
func Wrap(it Item) string {
	var b strings.Builder
	b.WriteString("<remote_message")
	attr := func(name, value string, always bool) {
		v := cleanAttr(value)
		if v == "" && !always {
			return
		}
		b.WriteString(" ")
		b.WriteString(name)
		b.WriteString(`="`)
		b.WriteString(attrEscaper.Replace(v))
		b.WriteString(`"`)
	}
	attr("from", it.Alias, true)
	attr("session", it.Session, false)
	attr("trust", it.Trust, true)
	attr("id", it.ID, true)
	attr("kind", it.Kind, true)
	attr("task_id", it.TaskID, false)
	b.WriteString(">\n")
	b.WriteString(bodyEscaper.Replace(stripInvisible(strings.ToValidUTF8(it.Body, "�"))))
	b.WriteString("\n</remote_message>")
	return b.String()
}

// IsInvisible reports whether r is one of the characters Wrap strips from
// peer text (see isInvisible). Other packages that show peer text, such as
// the CLI, use it to strip the same list.
func IsInvisible(r rune) bool { return isInvisible(r) }

// isInvisible reports characters that can hide or reorder text an agent
// reads: Unicode tag characters U+E0000-E007F ("ASCII smuggling"), bidi
// embeddings/overrides U+202A-202E and isolates U+2066-2069, zero-width
// characters and LRM/RLM U+200B-200F, and the BOM U+FEFF.
func isInvisible(r rune) bool {
	switch {
	case r >= 0xE0000 && r <= 0xE007F,
		r >= 0x202A && r <= 0x202E,
		r >= 0x2066 && r <= 0x2069,
		r >= 0x200B && r <= 0x200F,
		r == 0xFEFF:
		return true
	}
	return false
}

// stripInvisible removes every rune for which isInvisible is true.
func stripInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		if isInvisible(r) {
			return -1
		}
		return r
	}, s)
}

// CleanAttr is the cleaning Wrap applies to attribute values. Views use it
// for peer-chosen names they return outside the wrapper, such as a peer's
// session name.
func CleanAttr(s string) string { return cleanAttr(s) }

// cleanAttr drops control (Cc), format (Cf, e.g. bidi overrides and
// zero-width), line/paragraph separator (Zl, Zp) and invisible (see
// isInvisible) characters and invalid UTF-8, then caps the length in runes.
func cleanAttr(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || isInvisible(r) {
			continue
		}
		if n == MaxAttrRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
