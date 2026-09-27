package fakeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/ipc"
)

// Environment of the modes that talk to the daemon.
const (
	// EnvSocket is the daemon socket (the MCP config names CRAVV_HOME, and
	// test daemons use their own socket names).
	EnvSocket = "CRAVV_FAKE_AGENT_SOCKET"
	// EnvProbe is a JSON Probe for mode probe.
	EnvProbe = "CRAVV_FAKE_AGENT_PROBE"
)

// Probe names another session's task and link for mode probe to try.
type Probe struct {
	OtherTask string `json:"other_task,omitempty"`
	OtherLink int64  `json:"other_link,omitempty"`
}

func init() {
	actions["reply"] = func(rec *Record) { rec.Results = act(false, rec.Token, rec.Prompt) }
	actions["probe"] = func(rec *Record) { rec.Results = act(true, rec.Token, rec.Prompt) }
}

var (
	taskRE = regexp.MustCompile(`task_id="([0-9A-Z]{26})"`)
	linkRE = regexp.MustCompile(`link=(\d+)`)
)

// act binds to the managed session with the run token, as `cravv-connect
// mcp` does, and answers: it completes the task the prompt names, or
// replies on the link. With probe it first tries what a run must not do.
// Every result is "ok" or the daemon's error kind.
func act(probe bool, token, prompt string) map[string]string {
	res := map[string]string{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := ipc.DialContext(ctx, os.Getenv(EnvSocket))
	if err != nil {
		res["dial"] = err.Error()
		return res
	}
	defer c.Close()
	dir, _ := os.Getwd()
	if probe {
		// A process inside the run that does not bind first is refused.
		if u, err := ipc.DialContext(ctx, os.Getenv(EnvSocket)); err == nil {
			res["unbound_status"] = "ok"
			if err := u.Call(ctx, ipc.MethodStatus, nil, nil); err != nil {
				var re *ipc.RemoteError
				if errors.As(err, &re) {
					res["unbound_status"] = re.Kind
				} else {
					res["unbound_status"] = err.Error()
				}
			}
			u.Close()
		}
	}
	call := func(name, method string, params any) {
		res[name] = "ok"
		if err := c.Call(ctx, method, params, nil); err != nil {
			var re *ipc.RemoteError
			if errors.As(err, &re) {
				res[name] = re.Kind
			} else {
				res[name] = err.Error()
			}
		}
	}
	call("register", ipc.MethodSessionRegister, ipc.SessionRegisterParams{Agent: "claude", ProjectDir: dir, PID: os.Getpid()})
	call("bind", ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: token})
	var link int64
	if m := linkRE.FindStringSubmatch(prompt); m != nil {
		fmt.Sscan(m[1], &link)
	}
	if probe {
		var p Probe
		_ = json.Unmarshal([]byte(os.Getenv(EnvProbe)), &p)
		call("rebind", ipc.MethodSessionRunBind, ipc.RunBindParams{RunToken: token})
		call("share", ipc.MethodSessionShare, ipc.SessionShareParams{Name: "escape"})
		call("connect", ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "mac/lead", Permission: "tasks-auto"})
		call("decide", ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: link, Accept: true})
		call("permit", ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: link, Permission: "tasks-auto"})
		call("offers", ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "mac", Label: "x", Folder: dir, Permission: "tasks-auto"})
		call("unlock", ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "guess"})
		call("pause", ipc.MethodPeerPause, ipc.AliasParams{Alias: "mac"})
		call("kill", ipc.MethodKill, nil)
		call("status", ipc.MethodStatus, nil)
		call("create_task", ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: link, Instructions: "work for me"})
		call("other_task", ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: p.OtherTask, Result: "not mine"})
		call("other_link", ipc.MethodChatSend, ipc.ChatSendParams{Link: p.OtherLink, Text: "not my link"})
		call("links", ipc.MethodLinks, nil)
	}
	if m := taskRE.FindStringSubmatch(prompt); m != nil {
		call("complete", ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: m[1], Result: "done by the fake agent in " + dir})
	} else if link != 0 {
		call("reply", ipc.MethodChatSend, ipc.ChatSendParams{Link: link, Text: "fake agent read your message"})
	}
	return res
}
