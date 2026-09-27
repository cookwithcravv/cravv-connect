package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/cookwithcravv/cravv-connect/internal/present"
)

// terminalSafe makes one line of text that may come from a peer safe to
// print: it removes every control character (including newline, carriage
// return, tab and ESC), Unicode format characters (bidi controls, zero-width
// characters), line and paragraph separators, and the invisible characters
// present.Wrap strips. The result cannot move the cursor, start a new line,
// hide text or reorder it.
func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) || present.IsInvisible(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "\uFFFD"))
}

// terminalBlock renders multi-line peer text (a task preview or full text)
// with every line passed through terminalSafe and prefixed with "| ", so no
// line of it can pass for a line the CLI printed. Tabs become four spaces.
func terminalBlock(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\t", "    ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "| " + terminalSafe(l)
	}
	return strings.Join(lines, "\n")
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func shortID(id string) string {
	if len(id) > 16 {
		return id[:16]
	}
	return id
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
