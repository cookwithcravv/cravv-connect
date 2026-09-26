package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/ipc"
)

// scenarioChat is one of the four chats of the scenario.
type scenarioChat struct {
	m        *mcpAgent
	seen     *[]string // every tool result this chat's model saw
	listener string
}

func newScenarioChat(t *testing.T, n *Node, name, visibility string, h *Human) *scenarioChat {
	t.Helper()
	m, seen := newClaudeAgent(t, n, "chat-"+name, h)
	return &scenarioChat{m: m, seen: seen, listener: shareChat(t, m, name, visibility)}
}

// sawOnly fails if the chat's model saw any marker of another link.
func (c *scenarioChat) sawOnly(t *testing.T, name string, others ...string) {
	t.Helper()
	for _, s := range *c.seen {
		for _, o := range others {
			if strings.Contains(s, o) {
				t.Errorf("%s saw %q: %s", name, o, s)
			}
		}
	}
}

// The user's setup: the Mac shares two chats (training and voice), the GPU
// box shares two (trainer and wakeword), and the links are
// mac/training <-> gpu-box/trainer and mac/voice <-> gpu-box/wakeword.
// Messages and tasks flow on each link, neither pair of chats sees the
// other's traffic, and closing gpu-box/trainer tells only mac/training.
func TestScenario_MacAndGPUBox(t *testing.T) {
	t.Parallel()
	r := NewRelay(t)
	mac := NewNode(t, r, "mac", NodeOptions{AdminToken: AdminToken})
	gpu := NewNode(t, r, "gpu-box", NodeOptions{})
	Pair(t, mac, gpu)
	trainerHuman, wakewordHuman := &Human{}, &Human{}
	training := newScenarioChat(t, mac, "training", "private", nil)
	voice := newScenarioChat(t, mac, "voice", "private", nil)
	trainer := newScenarioChat(t, gpu, "trainer", "all-peers", trainerHuman)
	wakeword := newScenarioChat(t, gpu, "wakeword", "all-peers", wakewordHuman)

	// training <-> trainer at tasks-auto: the GPU box's human grants it
	// with the password. voice <-> wakeword at tasks-ask: decided in the
	// wakeword chat's form.
	var trainLink, voiceLink ipc.LinkView
	training.m.decode("connect", map[string]any{"target": "gpu-box/trainer", "permission": "tasks-auto", "note": "SCN-TRAIN-NOTE"}, &trainLink)
	voice.m.decode("connect", map[string]any{"target": "gpu-box/wakeword", "permission": "tasks-ask", "note": "SCN-VOICE-NOTE"}, &voiceLink)
	trainIn := gpu.WaitLink(wait, "training's request", func(l ipc.LinkView) bool { return l.Session == "trainer" && l.State == "pending" })
	voiceIn := gpu.WaitLink(wait, "voice's request", func(l ipc.LinkView) bool { return l.Session == "wakeword" && l.State == "pending" })
	gpu.Decide(trainIn.Link, true, "")
	wakewordHuman.Answer(Choose("accept"))
	if text := wakeword.m.call("review_pending", nil); !strings.Contains(text, "accepted") {
		t.Fatalf("wakeword's review %q", text)
	}
	if text := trainer.m.call("review_pending", nil); text != "Nothing is waiting for a decision." {
		t.Fatalf("trainer was asked about another chat's request: %q", text)
	}
	mac.WaitLink(wait, "training linked", func(l ipc.LinkView) bool { return l.Link == trainLink.Link && l.State == "active" })
	mac.WaitLink(wait, "voice linked", func(l ipc.LinkView) bool { return l.Link == voiceLink.Link && l.State == "active" })
	for _, c := range []*scenarioChat{training, voice, trainer, wakeword} {
		c.m.call("check_inbox", nil)
	}

	// Traffic on each link.
	training.m.call("send_message", map[string]any{"link": trainLink.Link, "text": "SCN-TRAIN-MSG"})
	voice.m.call("send_message", map[string]any{"link": voiceLink.Link, "text": "SCN-VOICE-MSG"})
	var trainTask, voiceTask ipc.TaskCreateResult
	training.m.decode("create_task", map[string]any{"link": trainLink.Link, "instructions": "SCN-TRAIN-TASK run epoch 1"}, &trainTask)
	voice.m.decode("create_task", map[string]any{"link": voiceLink.Link, "instructions": "SCN-VOICE-TASK record samples"}, &voiceTask)

	// trainer works on its task at once (tasks-auto).
	Eventually(t, wait, "trainer gets its message and task", func() bool {
		trainer.m.call("check_inbox", nil)
		all := strings.Join(*trainer.seen, "\n")
		return strings.Contains(all, "SCN-TRAIN-MSG") && strings.Contains(all, "SCN-TRAIN-TASK")
	})
	trainer.m.call("claim_task", map[string]any{"task_id": trainTask.TaskID})
	trainer.m.call("complete_task", map[string]any{"task_id": trainTask.TaskID, "result": "SCN-TRAIN-RESULT"})
	trainer.m.call("send_message", map[string]any{"link": trainIn.Link, "text": "SCN-TRAINER-REPLY"})

	// wakeword's task waits for its human, who approves it in the form.
	Eventually(t, wait, "wakeword gets its message", func() bool {
		wakeword.m.call("check_inbox", nil)
		return strings.Contains(strings.Join(*wakeword.seen, "\n"), "SCN-VOICE-MSG")
	})
	wakewordHuman.Answer(Choose("accept"))
	Eventually(t, wait, "wakeword's human approves the task", func() bool {
		return strings.Contains(wakeword.m.call("review_pending", nil), "approved by the human")
	})
	if !strings.Contains(wakeword.m.call("check_inbox", nil), "SCN-VOICE-TASK") {
		t.Fatal("the approved task did not reach wakeword")
	}
	wakeword.m.call("claim_task", map[string]any{"task_id": voiceTask.TaskID})
	wakeword.m.call("complete_task", map[string]any{"task_id": voiceTask.TaskID, "result": "SCN-VOICE-RESULT"})
	wakeword.m.call("send_message", map[string]any{"link": voiceIn.Link, "text": "SCN-WAKEWORD-REPLY"})

	// Each Mac chat gets its own link's result and reply.
	for _, c := range []struct {
		chat  *scenarioChat
		task  string
		reply string
	}{{training, trainTask.TaskID, "SCN-TRAINER-REPLY"}, {voice, voiceTask.TaskID, "SCN-WAKEWORD-REPLY"}} {
		// The task turns done before its update reaches the inbox, so wait
		// for the update itself: an unread one would wake the listener below.
		var inbox string
		Eventually(t, wait, "the result and the reply", func() bool {
			inbox += c.chat.m.call("check_inbox", nil)
			done := strings.Contains(c.chat.m.call("get_task", map[string]any{"task_id": c.task}), `"state": "done"`)
			return done && strings.Contains(inbox, "state: done") && strings.Contains(strings.Join(*c.chat.seen, "\n"), c.reply)
		})
	}

	// Neither pair saw the other's traffic, and cannot reach it.
	voiceMarks := []string{"SCN-VOICE", "SCN-WAKEWORD"}
	trainMarks := []string{"SCN-TRAIN", "SCN-TRAINER"}
	training.sawOnly(t, "mac/training", voiceMarks...)
	trainer.sawOnly(t, "gpu-box/trainer", voiceMarks...)
	voice.sawOnly(t, "mac/voice", trainMarks...)
	wakeword.sawOnly(t, "gpu-box/wakeword", trainMarks...)
	for _, c := range []struct {
		chat *scenarioChat
		task string
	}{{voice, trainTask.TaskID}, {wakeword, trainTask.TaskID}, {training, voiceTask.TaskID}, {trainer, voiceTask.TaskID}} {
		if text, isErr := c.chat.m.try("get_task", map[string]any{"task_id": c.task}); !isErr || !strings.Contains(text, "not found") {
			t.Fatalf("another link's task was visible: %q", text)
		}
	}

	// Closing gpu-box/trainer tells mac/training and nobody else.
	trainingWakes := mac.ListenerProcess(t, training.listener)
	voiceWakes := mac.ListenerProcess(t, voice.listener)
	wakewordWakes := gpu.ListenerProcess(t, wakeword.listener)
	Silent(t, trainingWakes, 300*time.Millisecond, "everything read")
	start := time.Now()
	trainer.m.call("session_close", nil)
	w := Waited(t, trainingWakes, 5*time.Second, "training learns the close")
	if want := fmt.Sprintf("cravv-connect: 1 link notice on link %d from gpu-box. Call check_inbox.\n", trainLink.Link); w.Stdout != want {
		t.Fatalf("training's listener %q, want %q", w.Stdout, want)
	}
	t.Logf("mac/training learned of the close after %s", time.Since(start).Round(time.Millisecond))
	if l := mac.Link(trainLink.Link); l.State != "closed" || l.Reason != core.CloseSessionClosed {
		t.Fatalf("training's link %+v", l)
	}
	Silent(t, voiceWakes, time.Second, "mac/voice is not told")
	Silent(t, wakewordWakes, 0, "gpu-box/wakeword is not told")
	if l := mac.Link(voiceLink.Link); l.State != "active" {
		t.Fatalf("voice's link %+v", l)
	}
	if !strings.Contains(training.m.call("check_inbox", nil), "closed") {
		t.Fatal("training's inbox does not say the link closed")
	}
	// The other pair keeps talking.
	voice.m.call("send_message", map[string]any{"link": voiceLink.Link, "text": "SCN-VOICE-AFTER"})
	Waited(t, wakewordWakes, wait, "wakeword still gets messages")
}
