package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
)

// terminalSafe removes control characters (including ESC) from text that may
// come from a peer, so it cannot move the cursor or rewrite the terminal.
// Newlines and tabs are kept.
func terminalSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
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
