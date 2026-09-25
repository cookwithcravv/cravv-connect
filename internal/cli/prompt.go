package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// Prompter asks the person at the terminal. Passwords are only ever read here,
// never from argv or the environment.
type Prompter interface {
	// Password reads a line without echo.
	Password(prompt string) (string, error)
	// Line reads a line; an empty answer returns def.
	Line(prompt, def string) (string, error)
}

// ErrNoTerminal is returned when no controlling terminal is available.
var ErrNoTerminal = errors.New("this command needs a person at a terminal (no /dev/tty)")

// TTYPrompter talks to /dev/tty directly, so piped stdin or stdout (for
// example an agent running the CLI) can never supply the password.
type TTYPrompter struct{}

func openTTY() (*os.File, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, ErrNoTerminal
	}
	return f, nil
}

func (TTYPrompter) Password(prompt string) (string, error) {
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	defer tty.Close()
	fmt.Fprint(tty, prompt)
	b, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(tty)
	if err != nil {
		return "", ErrNoTerminal
	}
	return string(b), nil
}

func (TTYPrompter) Line(prompt, def string) (string, error) {
	tty, err := openTTY()
	if err != nil {
		return "", err
	}
	defer tty.Close()
	if def != "" {
		fmt.Fprintf(tty, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(tty, "%s: ", prompt)
	}
	line, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && line == "" {
		return "", ErrNoTerminal
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}
