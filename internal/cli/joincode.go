package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cravv/cravv-connect/internal/bindcode"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/joincode"
	"github.com/cravv/cravv-connect/internal/qrcode"
	"golang.org/x/term"
)

// isTerminal reports whether w is a terminal. The QR code is drawn only
// there: in a pipe or a file it is just noise. Tests replace it.
var isTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// daemonRelay returns the relay URL the daemon uses, or "" when it cannot
// tell (an older daemon, or no answer).
func daemonRelay(ctx context.Context, c Caller) string {
	var st ipc.StatusResult
	if err := c.Call(ctx, ipc.MethodStatus, nil, &st); err != nil {
		return ""
	}
	return st.RelayURL
}

// printPairingCode shows how another machine pairs with bind, a bind code
// made on relay: the join code (with its QR code when qr is set and w is a
// terminal) for a new machine, and the plain bind code for one already set up
// for this relay.
// Without a usable relay it shows the bind code alone.
func printPairingCode(w io.Writer, relay, bind string, qr bool) error {
	b, err := bindcode.Parse(bind)
	var jc joincode.Code
	if err == nil {
		jc, err = joincode.New(relay, b)
	}
	if err != nil {
		fmt.Fprintf(w, "Bind code: %s\n\n", bind)
		fmt.Fprintln(w, "On the other machine run:")
		fmt.Fprintf(w, "  cravv-connect join %s\n", bind)
	} else {
		fmt.Fprintf(w, "Join code: %s\n\n", jc)
		if qr && isTerminal(w) {
			// Upper case fits the QR alphanumeric mode; join codes parse in any case.
			if err := qrcode.Terminal(w, strings.ToUpper(jc.String())); err != nil {
				return err
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, "On a new machine run:")
		fmt.Fprintf(w, "  cravv-connect setup --join %s\n", jc)
		fmt.Fprintln(w, "On a machine already set up for this relay run:")
		fmt.Fprintf(w, "  cravv-connect join %s\n", bind)
	}
	fmt.Fprintln(w, "The code works once and expires in 10 minutes.")
	return nil
}

// joinBindCode returns the bind code to join with for code: a plain bind code
// unchanged, or the one inside a join code made on the daemon's relay. A join
// code for another relay is refused with advice.
func joinBindCode(ctx context.Context, c Caller, code string) (string, error) {
	if !joincode.Is(code) {
		return code, nil
	}
	bind, err := joincode.BindCode(code, daemonRelay(ctx, c))
	if errors.Is(err, joincode.ErrOtherRelay) {
		return "", fmt.Errorf("%w. To move this machine to that relay, run `cravv-connect setup --reset --join <code>` (you pair again with every peer).", err)
	}
	return bind, err
}
