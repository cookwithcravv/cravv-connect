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
// runes and XML-escaped. The body is XML-escaped (&, <, >) so it cannot
// close the tag or open a new one.
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
	b.WriteString(bodyEscaper.Replace(strings.ToValidUTF8(it.Body, "�")))
	b.WriteString("\n</remote_message>")
	return b.String()
}

// cleanAttr drops control (Cc) and format (Cf, e.g. bidi overrides and
// zero-width) characters and invalid UTF-8, then caps the length in runes.
func cleanAttr(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
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
