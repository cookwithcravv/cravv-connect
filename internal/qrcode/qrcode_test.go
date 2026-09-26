package qrcode

import (
	"bytes"
	"strings"
	"testing"

	"rsc.io/qr"
)

const sample = "CRAVV-JOIN:NB2HI4DTHIXS64TFNRQXSLTFPBQW24DMMUXGG33N:7K3F-9QXMTR2A"

// The terminal drawing must be exactly the encoder's modules: two module rows
// per line, dark modules drawn in the foreground (black) on a light (white)
// background, with a light quiet zone all round.
func TestTerminalDrawsTheModules(t *testing.T) {
	var buf bytes.Buffer
	if err := Terminal(&buf, sample); err != nil {
		t.Fatal(err)
	}
	code, err := qr.Encode(sample, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	n := code.Size + 2*Quiet
	dark := func(x, y int) bool {
		x, y = x-Quiet, y-Quiet
		return x >= 0 && y >= 0 && x < code.Size && y < code.Size && code.Black(x, y)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != (n+1)/2 {
		t.Fatalf("%d lines, want %d", len(lines), (n+1)/2)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, colors) || !strings.HasSuffix(line, reset) {
			t.Fatalf("line %d is not wrapped in the color codes: %q", i, line)
		}
		cells := []rune(strings.TrimSuffix(strings.TrimPrefix(line, colors), reset))
		if len(cells) != n {
			t.Fatalf("line %d has %d cells, want %d", i, len(cells), n)
		}
		for x, r := range cells {
			top, bottom := dark(x, 2*i), dark(x, 2*i+1)
			want := map[[2]bool]rune{{false, false}: ' ', {true, false}: '▀', {false, true}: '▄', {true, true}: '█'}[[2]bool{top, bottom}]
			if r != want {
				t.Fatalf("line %d cell %d = %q, want %q", i, x, r, want)
			}
		}
	}
}

// A join code upper-cased fits the QR alphanumeric mode, which keeps the
// drawing small enough for an ordinary terminal.
func TestJoinCodeFitsATerminal(t *testing.T) {
	long := "CRAVV-JOIN:" + strings.Repeat("A", 104) + ":7K3F-9QXMTR2A" // a 64-character relay origin
	var buf bytes.Buffer
	if err := Terminal(&buf, long); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if w := len([]rune(strings.TrimSuffix(strings.TrimPrefix(lines[0], colors), reset))); w > 60 || len(lines) > 30 {
		t.Fatalf("drawing is %d wide and %d high", w, len(lines))
	}
}

func TestTerminalRefusesEmptyText(t *testing.T) {
	if err := Terminal(&bytes.Buffer{}, ""); err == nil {
		t.Fatal("empty text accepted")
	}
}
