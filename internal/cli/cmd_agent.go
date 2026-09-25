package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/cravv/cravv-connect/internal/ipc"
	"github.com/spf13/cobra"
)

// CLIAgent is the agent name for sessions registered by the JSON commands.
const CLIAgent = "cli"

func init() {
	Register(newSendCmd)
	Register(newInboxCmd)
	Register(newWaitCmd)
	Register(newTaskCmd)
}

type jsonError struct {
	Error string `json:"error"`
	Kind  string `json:"kind"`
}

// agentRun registers a `cli@<dir>` session, runs fn, and prints its result as
// JSON. Errors are printed as JSON on stdout too, so agents parse one stream.
func agentRun(ctx context.Context, env *Env, fn func(c Caller) (any, error)) error {
	res, err := func() (any, error) {
		c, err := connect(ctx, env)
		if err != nil {
			return nil, err
		}
		defer c.Close()
		dir, err := env.Getwd()
		if err != nil {
			return nil, err
		}
		if err := c.Call(ctx, ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: CLIAgent, ProjectDir: dir, PID: os.Getpid()}, nil); err != nil {
			return nil, err
		}
		return fn(c)
	}()
	if err != nil {
		kind := ipc.KindOf(err)
		var re *ipc.RemoteError
		if errors.As(err, &re) {
			kind = re.Kind
		}
		printJSON(env.Stdout, jsonError{Error: err.Error(), Kind: kind})
		return errSilent
	}
	return printJSON(env.Stdout, res)
}

// textArg returns s, or all of stdin when s is "-".
func textArg(env *Env, s string) (string, error) {
	if s != "-" {
		return s, nil
	}
	b, err := io.ReadAll(env.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// call is a typed helper for agent commands.
func call[R any](ctx context.Context, c Caller, method string, params any) (any, error) {
	var r R
	if err := c.Call(ctx, method, params, &r); err != nil {
		return nil, err
	}
	return r, nil
}

func newSendCmd(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "send <to> <text|->",
		Short: "Send a chat message (JSON output; to is alias or alias/session)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := textArg(env, args[1])
			if err != nil {
				return err
			}
			return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
				return call[ipc.IDResult](cmd.Context(), c, ipc.MethodChatSend, ipc.ChatSendParams{To: args[0], Text: text})
			})
		},
	}
}

func newInboxCmd(env *Env) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "Read unread items for this folder's cli session (JSON output)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
				return call[ipc.InboxResult](cmd.Context(), c, ipc.MethodInboxCheck, ipc.InboxCheckParams{Limit: limit})
			})
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum items (default 50)")
	return cmd
}

func newWaitCmd(env *Env) *cobra.Command {
	var timeout int
	cmd := &cobra.Command{
		Use:   "wait",
		Short: "Wait up to 50 seconds for a new item (JSON output)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
				return call[ipc.InboxResult](cmd.Context(), c, ipc.MethodInboxWait, ipc.InboxWaitParams{TimeoutS: timeout})
			})
		},
	}
	cmd.Flags().IntVar(&timeout, "timeout", 50, "seconds to wait, at most 50")
	return cmd
}

func newTaskCmd(env *Env) *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Create and work on tasks (JSON output)"}
	var createFiles, completeFiles []string

	create := &cobra.Command{
		Use:   "create <to> <instructions|->",
		Short: "Send a task to a peer",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := textArg(env, args[1])
			if err != nil {
				return err
			}
			return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
				return call[ipc.TaskCreateResult](cmd.Context(), c, ipc.MethodTaskCreate,
					ipc.TaskCreateParams{To: args[0], Instructions: text, FilePaths: createFiles})
			})
		},
	}
	create.Flags().StringArrayVar(&createFiles, "file", nil, "attach a file (repeatable)")

	byID := func(use, short, method string) *cobra.Command {
		return &cobra.Command{
			Use: use + " <task-id>", Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
					return call[ipc.TaskView](cmd.Context(), c, method, ipc.TaskIDParams{TaskID: args[0]})
				})
			},
		}
	}

	update := &cobra.Command{
		Use: "update <task-id> <note|->", Short: "Send a progress note", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			note, err := textArg(env, args[1])
			if err != nil {
				return err
			}
			return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
				return call[ipc.TaskView](cmd.Context(), c, ipc.MethodTaskUpdate, ipc.TaskUpdateParams{TaskID: args[0], Note: note})
			})
		},
	}
	complete := &cobra.Command{
		Use: "complete <task-id> <result|->", Short: "Finish a claimed task with a result", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := textArg(env, args[1])
			if err != nil {
				return err
			}
			return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
				return call[ipc.TaskView](cmd.Context(), c, ipc.MethodTaskComplete,
					ipc.TaskCompleteParams{TaskID: args[0], Result: result, FilePaths: completeFiles})
			})
		},
	}
	complete.Flags().StringArrayVar(&completeFiles, "file", nil, "attach a result file (repeatable)")
	fail := &cobra.Command{
		Use: "fail <task-id> <reason>", Short: "Fail a claimed task", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return agentRun(cmd.Context(), env, func(c Caller) (any, error) {
				return call[ipc.TaskView](cmd.Context(), c, ipc.MethodTaskFail, ipc.TaskFailParams{TaskID: args[0], Reason: args[1]})
			})
		},
	}
	cmd.AddCommand(create,
		byID("get", "Show a task", ipc.MethodTaskGet),
		byID("claim", "Claim a task (fails if already claimed)", ipc.MethodTaskClaim),
		update, complete, fail,
		byID("cancel", "Cancel a task you sent", ipc.MethodTaskCancel),
	)
	return cmd
}
