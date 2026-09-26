package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/spf13/cobra"
)

func init() { Register(newListenCmd) }

// Listener retry policy after the daemon connection drops (a daemon
// restart): waits double from listenBackoffMin up to listenBackoffMax, and
// the listener gives up after listenMaxFailures failures in a row. A listen
// call that stayed up for listenBackoffMax counts as healthy and resets
// the count.
var (
	listenBackoffMin  = time.Second
	listenBackoffMax  = 30 * time.Second
	listenMaxFailures = 8
)

// maxWakeToken caps what is read as a wake token.
const maxWakeToken = 1024

// Lines the listener prints. Each is one line; the agent reads it when the
// background command exits.
const (
	listenClosedLine  = "cravv-connect: this chat's session is closed, so nothing more will arrive. Share the chat again (session_share) to keep receiving."
	listenInvalidLine = "cravv-connect: the wake token is not valid (the session may be closed). Share the chat again (session_share) and start the listener it gives you."
	listenLostLine    = "cravv-connect: lost the connection to the daemon and could not get it back. Start the listener again once the daemon runs (cravv-connect daemon start)."
)

func newListenCmd(env *Env) *cobra.Command {
	var wakeFile string
	cmd := &cobra.Command{
		Use:   "listen",
		Short: "Wait until this chat's shared session has something new, print one line and exit (wake token on stdin)",
		Long: "Blocks until the shared session the wake token belongs to has something new, then prints one line " +
			"naming the local machine alias and link number and exits 0. The token is read from stdin, or from a " +
			"file only you can read (--wake-file), never from an argument. Run it as a background command.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := readWakeToken(env, wakeFile)
			if err != nil {
				fmt.Fprintln(env.Stdout, "cravv-connect: "+err.Error())
				return errSilent
			}
			line, err := listen(cmd.Context(), env, token)
			fmt.Fprintln(env.Stdout, line)
			return err
		},
	}
	cmd.Flags().StringVar(&wakeFile, "wake-file", "", "read the wake token from this file (it must be yours and not readable by others)")
	return cmd
}

// readWakeToken reads the token from the wake file or stdin.
func readWakeToken(env *Env, path string) (string, error) {
	var r io.Reader = env.Stdin
	if path != "" {
		f, err := openWakeFile(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, maxWakeToken+1))
	if err != nil {
		return "", fmt.Errorf("read the wake token: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" || len(b) > maxWakeToken || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("no wake token: pipe the token from session_share to stdin, or pass --wake-file")
	}
	return token, nil
}

// openWakeFile opens path only if it is a regular file (not a symlink)
// owned by this user and closed to group and others. The checks run on
// the opened descriptor, so the path cannot be swapped between a check and
// the open: O_NOFOLLOW refuses a symlink, O_NONBLOCK keeps a FIFO from
// blocking the open, and fstat looks at what was actually opened.
func openWakeFile(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("wake file %s is a symbolic link", path)
	}
	if err != nil {
		return nil, fmt.Errorf("wake file: %w", &os.PathError{Op: "open", Path: path, Err: err})
	}
	if err := checkWakeFD(fd, path); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

// checkWakeFD checks the opened wake file and makes it blocking again.
func checkWakeFD(fd int, path string) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return fmt.Errorf("wake file: %w", err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("wake file %s is not a regular file", path)
	}
	if perm := os.FileMode(st.Mode).Perm(); perm&0o077 != 0 {
		return fmt.Errorf("wake file %s can be read by other users (mode %o); it must be 0600", path, perm)
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("wake file %s belongs to another user", path)
	}
	return syscall.SetNonblock(fd, false)
}

// listen blocks on session.listen until something is pending. It returns
// the line to print and errSilent when the listener failed.
func listen(ctx context.Context, env *Env, token string) (string, error) {
	failures := 0
	wait := listenBackoffMin
	for {
		started := time.Now()
		r, err := listenOnce(ctx, env, token)
		switch {
		case err == nil && r.Closed:
			return listenClosedLine, nil
		case err == nil && r.Unread > 0:
			return listenLine(r), nil
		case err == nil:
			continue // the daemon gave up waiting; ask again
		case errors.Is(err, core.ErrNotFound):
			return listenInvalidLine, errSilent
		case errors.Is(err, ipc.ErrBadRequest) || errors.Is(err, core.ErrKilled) || ctx.Err() != nil:
			return "cravv-connect: the listener stopped: " + terminalSafe(userMessage(err)), errSilent
		}
		// Anything else is taken as the daemon going away (a restart ends
		// the call with a closed connection or a cancelled request).
		if time.Since(started) >= listenBackoffMax {
			failures, wait = 0, listenBackoffMin // the connection was healthy until now
		}
		failures++
		if failures >= listenMaxFailures {
			return listenLostLine, errSilent
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return listenLostLine, errSilent
		}
		wait = min(2*wait, listenBackoffMax)
	}
}

// listenOnce runs one session.listen call on a fresh connection.
func listenOnce(ctx context.Context, env *Env, token string) (ipc.ListenResult, error) {
	c, err := connect(ctx, env)
	if err != nil {
		return ipc.ListenResult{}, err
	}
	defer c.Close()
	var r ipc.ListenResult
	err = c.Call(ctx, ipc.MethodSessionListen, ipc.SessionListenParams{WakeToken: token}, &r)
	return r, err
}

// listenLine names only local aliases and link numbers.
func listenLine(r ipc.ListenResult) string {
	ps := make([]present.Pending, 0, len(r.Pending))
	for _, p := range r.Pending {
		ps = append(ps, present.Pending{Link: p.Link, Machine: p.Machine, Kind: p.Kind, Count: p.Count})
	}
	if line := present.PendingLine(ps); line != "" {
		return line
	}
	return fmt.Sprintf("cravv-connect: %d new %s. Call check_inbox.", r.Unread, pluralWord(r.Unread, "item", "items"))
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
