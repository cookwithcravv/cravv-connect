// Package qrcode draws QR codes for join codes. It uses rsc.io/qr (BSD-3-Clause,
// pure Go) for the encoding and draws the modules itself.
package qrcode

import (
	"bufio"
	"errors"
	"io"

	"rsc.io/qr"
)

// Quiet is the light border around the code, in modules: the four the QR
// specification asks for.
const Quiet = 4

// ANSI black on white for every line, so the code scans the same in dark and
// light terminals (scanners want dark modules on a light background).
const (
	colors = "\x1b[30;47m"
	reset  = "\x1b[0m"
)

// grid returns the modules of text's QR code (error correction level M),
// true for dark, with the quiet zone included.
func grid(text string) ([][]bool, error) {
	if text == "" {
		return nil, errors.New("qrcode: empty text")
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return nil, err
	}
	n := code.Size + 2*Quiet
	g := make([][]bool, n)
	for y := range g {
		g[y] = make([]bool, n)
		for x := range g[y] {
			cx, cy := x-Quiet, y-Quiet
			g[y][x] = cx >= 0 && cy >= 0 && cx < code.Size && cy < code.Size && code.Black(cx, cy)
		}
	}
	return g, nil
}

// Terminal writes text as a QR code for a terminal: each line holds two rows
// of modules drawn with Unicode half blocks. Upper-case text (digits, A to Z
// and " $%*+-./:") uses the compact alphanumeric mode.
func Terminal(w io.Writer, text string) error {
	g, err := grid(text)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	for y := 0; y < len(g); y += 2 {
		bw.WriteString(colors)
		for x := range g[y] {
			top, bottom := g[y][x], y+1 < len(g) && g[y+1][x]
			switch {
			case top && bottom:
				bw.WriteString("█")
			case top:
				bw.WriteString("▀")
			case bottom:
				bw.WriteString("▄")
			default:
				bw.WriteByte(' ')
			}
		}
		bw.WriteString(reset + "\n")
	}
	return bw.Flush()
}
