package e2e

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/core"
	"github.com/cookwithcravv/cravv-connect/internal/ipc"
	"github.com/cookwithcravv/cravv-connect/internal/relayserver"
)

// offerFolder returns a folder an offer may name (absolute, symlinks
// resolved, not the home folder).
func offerFolder(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestAcceptance_7_UI pins criterion 7, "UI": "Devices, sessions, links
// and managed-session rules can be seen and changed both from the chat and
// from a local web page."
//
// From the chat (the MCP tools) the agent sees paired devices, the
// sessions and offers a device shows, and its own links, and changes its
// links (connect, restrict, disconnect). Changing a device or a rule is
// for the human (spec 7.3 and 10), so the chat sees those changes but
// makes them on the web page, which shows and changes all four.
func TestAcceptance_7_UI(t *testing.T) {
	t.Parallel()
	_, mac, gpu := NewPair(t)
	lead, _ := newClaudeAgent(t, mac, "chat-lead", nil)
	shareChat(t, lead, "lead", "private")
	gpu.Share("claude", "trainer", "all-peers")
	page := OpenUI(t, gpu)

	// Devices: the chat sees them; the page pauses and resumes one.
	if text := lead.call("machines", nil); !strings.Contains(text, `"alias": "bob"`) || !strings.Contains(text, `"paused_by_peer": false`) {
		t.Fatalf("machines %s", text)
	}
	wantPage(t, page.Get("/devices"), "<td>alice</td>")
	wantPage(t, page.Submit("/devices", "/devices/pause", url.Values{"alias": {"alice"}}), "Paused alice.")
	Eventually(t, wait, "the chat sees the pause", func() bool {
		return strings.Contains(lead.call("machines", nil), `"paused_by_peer": true`)
	})
	wantPage(t, page.Submit("/devices", "/devices/resume", url.Values{"alias": {"alice"}}), "Resumed alice.")
	Eventually(t, wait, "the chat sees the resume", func() bool {
		return strings.Contains(lead.call("machines", nil), `"paused_by_peer": false`)
	})

	// Managed-session rules: set on the page (with the password), seen
	// from the chat as the device's offers.
	folder := offerFolder(t)
	rule := url.Values{"machine": {"alice"}, "label": {"acc7-rule"}, "folder": {folder}, "permission": {"messages"}, "run_mode": {"read-only"}}
	wantPage(t, page.Submit("/managed", "/managed/offers/set", rule), "This needs your login password.")
	rule.Set("password", Password)
	wantPage(t, page.Submit("/managed", "/managed/offers/set", rule), "Offer acc7-rule to alice: "+folder+" (read-only, messages).")
	wantPage(t, page.Get("/managed"), "<td>alice</td><td>acc7-rule</td>")
	var listed ipc.SessionsListResult
	lead.decode("sessions", map[string]any{"machine": "bob"}, &listed)
	if len(listed.Sessions) != 1 || listed.Sessions[0].Name != "trainer" || len(listed.Offers) != 1 || listed.Offers[0].Label != "acc7-rule" {
		t.Fatalf("the chat sees %+v", listed)
	}
	remove := url.Values{"machine": {"alice"}, "label": {"acc7-rule"}, "password": {Password}}
	wantPage(t, page.Submit("/managed", "/managed/offers/remove", remove), "Removed offer acc7-rule to alice")
	var after ipc.SessionsListResult
	lead.decode("sessions", map[string]any{"machine": "bob"}, &after)
	if len(after.Offers) != 0 {
		t.Fatalf("the chat still sees offers %+v", after.Offers)
	}

	// Sessions and links: the chat connects, the page accepts (tasks-auto
	// needs the password) and both see and change the link.
	var out ipc.LinkView
	lead.decode("connect", map[string]any{"target": "bob/trainer", "permission": "tasks-auto", "note": "ACC7"}, &out)
	in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" && l.Direction == "in" })
	num := strconv.FormatInt(in.Link, 10)
	wantPage(t, page.Get("/approvals"), "<strong>alice/lead</strong> asks to link with your session <strong>trainer</strong>")
	wantPage(t, page.Submit("/approvals", "/approvals/link/accept", url.Values{"link": {num}, "password": {Password}}),
		"Accepted link "+num+": alice/lead may now use tasks-auto.")
	Eventually(t, wait, "the chat sees the link", func() bool {
		text := lead.call("links", nil)
		return strings.Contains(text, `"state": "active"`) && strings.Contains(text, `"permission_out": "tasks-auto"`)
	})
	wantPage(t, page.Get("/sessions"), "<td>trainer</td>", "alice/lead", "<td>active</td>")
	wantPage(t, page.Submit("/sessions", "/sessions/restrict", url.Values{"link": {num}, "permission": {"tasks-ask"}}), "Link "+num+" now allows tasks-ask.")
	Eventually(t, wait, "the chat sees the lower permission", func() bool {
		return strings.Contains(lead.call("links", nil), `"permission_out": "tasks-ask"`)
	})
	if text := lead.call("restrict", map[string]any{"link": out.Link, "permission": "messages"}); text != "Link "+strconv.FormatInt(out.Link, 10)+" now allows messages." {
		t.Fatalf("restrict from the chat %q", text)
	}
	gpu.WaitLink(wait, "the page's machine sees the chat's restrict", func(l ipc.LinkView) bool { return l.Link == in.Link && l.PermissionOut == "messages" })
	lead.call("disconnect", map[string]any{"link": out.Link})
	gpu.WaitLink(wait, "the page's machine sees the disconnect", func(l ipc.LinkView) bool { return l.Link == in.Link && l.State == "closed" })
	wantPage(t, page.Get("/sessions"), "<td>closed: closed_by_peer</td>")
}

// TestAcceptance_8_SecurityHolds pins criterion 8, "Security": "Pairing,
// broad grants and managed-session rules keep the v1 password gate.", "The
// kill switch, pause and unpair keep working.", "The relay stays blind."
// and "Session identity cannot be taken over by local processes that were
// not given it."
func TestAcceptance_8_SecurityHolds(t *testing.T) {
	t.Parallel()

	t.Run("pairing, broad grants and rules need the password", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		trainer := gpu.Share("claude", "trainer", "all-peers")
		lead := mac.Share("claude", "lead", "private")
		plain := gpu.Conn()
		wantKind(t, TryCall(plain, ipc.MethodPairStart, nil, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodJoinStart, ipc.JoinStartParams{Code: "CRAVV-0000-0000-0000"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "alice", Label: "x", Folder: offerFolder(t), Permission: "messages"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodOffersRemove, ipc.OfferRemoveParams{Machine: "alice", Label: "x"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodAuthUnlock, ipc.UnlockParams{Password: "wrong"}, nil), ipc.KindBadPassword)

		Connect(t, lead, "bob/trainer", "tasks-auto", "")
		in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
		// Accepting at tasks-auto: not from the chat, not without the password.
		wantKind(t, TryCall(trainer.C, ipc.MethodReviewDecide, ipc.ReviewDecideParams{Item: "link-" + strconv.FormatInt(in.Link, 10), Accept: true}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodLinkDecide, ipc.LinkDecideParams{Link: in.Link, Accept: true}, nil), ipc.KindAuthRequired)
		// Accepting lower from the terminal, then raising, needs it too.
		gpu.Decide(in.Link, true, "messages")
		wantKind(t, TryCall(trainer.C, ipc.MethodLinkRestrict, ipc.LinkPermissionParams{Link: in.Link, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
		wantKind(t, TryCall(plain, ipc.MethodLinkPermit, ipc.LinkPermissionParams{Link: in.Link, Permission: "tasks-auto"}, nil), ipc.KindAuthRequired)
		if l := gpu.Link(in.Link); l.PermissionIn != "messages" {
			t.Fatalf("raised without the password: %+v", l)
		}
		// An agent's connection never edits rules, even unlocked: only the
		// owner's CLI or web UI may.
		agent := trainer.C
		Unlock(t, agent)
		wantKind(t, TryCall(agent, ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "alice", Label: "x", Folder: offerFolder(t), Permission: "messages"}, nil), ipc.KindBadRequest)
	})

	t.Run("kill switch, pause and unpair cut links off", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		killed := LinkUp(t, mac, gpu, "tasks-auto")
		Call(t, killed.B.C, ipc.MethodKill, nil, nil) // the agent's own kill_switch works
		wantKind(t, TryCall(killed.B.C, ipc.MethodChatSend, ipc.ChatSendParams{Link: killed.BNum, Text: "x"}, nil), ipc.KindKilled)
		mac.WaitLink(wait, "killed", func(l ipc.LinkView) bool {
			return l.Link == killed.ANum && l.State == "closed" && l.Reason == core.CloseKilled
		})
		wantKind(t, TryCall(gpu.Conn(), ipc.MethodResume, nil, nil), ipc.KindAuthRequired)
		Call(t, gpu.Unlocked(), ipc.MethodResume, nil, nil)
		if l := gpu.Link(killed.BNum); l.State != "closed" {
			t.Fatalf("a link came back after resume: %+v", l)
		}

		paused := LinkChats(t, mac, gpu, mac.Share("claude", "p-lead", "private"), gpu.Share("claude", "p-trainer", "all-peers"), "messages")
		Call(t, mac.Conn(), ipc.MethodPeerPause, ipc.AliasParams{Alias: "bob"}, nil)
		gpu.WaitLink(wait, "paused", func(l ipc.LinkView) bool {
			return l.Link == paused.BNum && l.State == "closed" && l.Reason == core.ClosePaused
		})
		wantKind(t, TryCall(paused.A.C, ipc.MethodLinkConnect, ipc.LinkConnectParams{Target: "bob/p-trainer", Permission: "messages"}, nil), ipc.KindPaused)
		Call(t, mac.Conn(), ipc.MethodPeerResume, ipc.AliasParams{Alias: "bob"}, nil)

		unpaired := LinkChats(t, mac, gpu, mac.Share("claude", "u-lead", "private"), gpu.Share("claude", "u-trainer", "all-peers"), "messages")
		Call(t, gpu.Conn(), ipc.MethodPeerUnpair, ipc.AliasParams{Alias: "alice"}, nil)
		mac.WaitLink(wait, "unpaired", func(l ipc.LinkView) bool {
			return l.Link == unpaired.ANum && l.State == "closed" && l.Reason == core.CloseUnpaired
		})
		Eventually(t, wait, "both forget each other", func() bool {
			_, a := mac.PeerView("bob")
			_, b := gpu.PeerView("alice")
			return !a && !b
		})
	})

	t.Run("the relay stays blind", func(t *testing.T) {
		t.Parallel()
		rec := &recordingBackend{}
		r := NewRelayWith(t, func(b relayserver.Backend) relayserver.Backend { rec.Backend = b; return rec })
		mac := NewNode(t, r, "acc8-mac-q1", NodeOptions{AdminToken: AdminToken})
		gpu := NewNode(t, r, "acc8-gpu-q2", NodeOptions{})
		Pair(t, mac, gpu)
		const (
			purpose = "ACC8-PURPOSE-3c1f"
			note    = "ACC8-NOTE-8d2e"
			label   = "acc8-offer-5b7a"
			chat    = "ACC8-CHAT-1a9c"
			instr   = "ACC8-TASK-6e4d"
			result  = "ACC8-RESULT-2f0b"
		)
		Call(t, gpu.Unlocked(), ipc.MethodOffersSet, ipc.OfferSetParams{Machine: "acc8-mac-q1", Label: label, Folder: offerFolder(t), Permission: "messages"}, nil)
		c, _ := gpu.Session("claude")
		var shared ipc.ShareResult
		Call(t, c, ipc.MethodSessionShare, ipc.SessionShareParams{Name: "acc8-trainer-z9", Purpose: purpose, Visibility: "all-peers"}, &shared)
		trainer := &SharedChat{C: c, Name: "acc8-trainer-z9", Res: shared}
		lead := mac.Share("claude", "acc8-lead-y8", "private")
		var listed ipc.SessionsListResult
		Call(t, lead.C, ipc.MethodSessionsList, ipc.MachineParams{Machine: "acc8-gpu-q2"}, &listed)
		if len(listed.Sessions) != 1 || len(listed.Offers) != 1 {
			t.Fatalf("discovery %+v", listed)
		}
		out := Connect(t, lead, "acc8-gpu-q2/acc8-trainer-z9", "tasks-auto", note)
		in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
		gpu.Decide(in.Link, true, "")
		mac.WaitLink(wait, "active", func(l ipc.LinkView) bool { return l.Link == out.Link && l.State == "active" })
		id := sendChat(t, lead.C, out.Link, chat)
		WaitItem(t, trainer.C, wait, "the chat", isChat(id))
		var task ipc.TaskCreateResult
		Call(t, lead.C, ipc.MethodTaskCreate, ipc.TaskCreateParams{Link: out.Link, Instructions: instr}, &task)
		WaitItem(t, trainer.C, wait, "the task", func(it ipc.InboxView) bool { return it.TaskID == task.TaskID })
		Call(t, trainer.C, ipc.MethodTaskClaim, ipc.TaskIDParams{TaskID: task.TaskID}, nil)
		Call(t, trainer.C, ipc.MethodTaskComplete, ipc.TaskCompleteParams{TaskID: task.TaskID, Result: result}, nil)
		taskState(t, lead.C, task.TaskID, "done")
		Call(t, lead.C, ipc.MethodSessionClose, nil, nil)
		gpu.WaitLink(wait, "closed", func(l ipc.LinkView) bool { return l.Link == in.Link && l.State == "closed" })

		frames, chunks := rec.snapshot()
		if len(frames) < 8 {
			t.Fatalf("recorded %d frames: the recorder missed traffic", len(frames))
		}
		secrets := []string{purpose, note, label, chat, instr, result, "acc8-mac-q1", "acc8-gpu-q2", "acc8-trainer-z9", "acc8-lead-y8",
			shared.WakeToken, shared.ReattachToken}
		for _, blobs := range [][][]byte{frames, chunks} {
			for i, data := range blobs {
				for _, s := range secrets {
					if bytes.Contains(data, []byte(s)) {
						t.Errorf("the relay saw %q in stored item %d", s, i)
					}
				}
			}
		}
	})

	t.Run("session identity cannot be taken over", func(t *testing.T) {
		t.Parallel()
		_, mac, gpu := NewPair(t)
		trainer, _ := newClaudeAgent(t, gpu, "chat-trainer", nil)
		listener := shareChat(t, trainer, "trainer", "all-peers")
		lead := mac.Share("claude", "lead", "private")
		Connect(t, lead, "bob/trainer", "messages", "")
		in := gpu.WaitLink(wait, "the request", func(l ipc.LinkView) bool { return l.State == "pending" })
		gpu.Decide(in.Link, true, "")

		// The listener's command line names a file, never a token; the
		// file is the user's alone.
		file := strings.TrimPrefix(listener, "cravv-connect listen --wake-file ")
		token, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 || strings.Contains(listener, strings.TrimSpace(string(token))) {
			t.Fatalf("wake file %s: %v %v", file, fi, err)
		}

		// Another local process of the same user, on its own connection:
		// no session (not_shared) and no link of the chat's.
		other, _ := gpu.Session("claude")
		wantKind(t, TryCall(other, ipc.MethodInboxCheck, ipc.InboxCheckParams{}, nil), ipc.KindNotShared)
		wantKind(t, TryCall(other, ipc.MethodChatSend, ipc.ChatSendParams{Link: in.Link, Text: "impostor"}, nil), ipc.KindNotShared)
		wantKind(t, TryCall(other, ipc.MethodLinkDisconnect, ipc.LinkParams{Link: in.Link}, nil), ipc.KindNotShared)
		// Guessing or reusing tokens: the wake token only counts, and is
		// no reattach token.
		for _, guess := range []string{strings.TrimSpace(string(token)), "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
			wantKind(t, TryCall(other, ipc.MethodSessionReattach, ipc.SessionReattachParams{ReattachToken: guess}, nil), ipc.KindNotFound)
		}
		var counts json.RawMessage
		Call(t, gpu.Conn(), ipc.MethodSessionListen, ipc.SessionListenParams{WakeToken: strings.TrimSpace(string(token)), TimeoutS: 1}, &counts)
		var keys map[string]any
		if err := json.Unmarshal(counts, &keys); err != nil {
			t.Fatal(err)
		}
		for k := range keys {
			if k != "unread" && k != "requests" && k != "approvals" && k != "pending" && k != "closed" {
				t.Fatalf("the wake token revealed %q: %s", k, counts)
			}
		}
		if l := gpu.Link(in.Link); l.State != "active" {
			t.Fatalf("the link changed: %+v", l)
		}
		trainer.call("links", nil) // the chat still holds its session
	})
}
