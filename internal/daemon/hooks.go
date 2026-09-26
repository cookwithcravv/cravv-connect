package daemon

import (
	"context"
	"fmt"
	"regexp"
	"sync"

	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/present"
	"github.com/cravv/cravv-connect/internal/store"
)

// MaxStopBlocks is how many times in a row the Stop hook keeps a chat
// going (v2 spec 7.1).
const MaxStopBlocks = 2

// Hook events the daemon distinguishes.
const (
	HookStop             = "Stop"
	HookSubagentStop     = "SubagentStop"
	HookUserPromptSubmit = "UserPromptSubmit"
)

// agentSessionRE accepts the IDs agents give their chats (Claude Code's
// session_id is a UUID).
var agentSessionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// ValidAgentSession reports whether id looks like an agent's chat ID.
func ValidAgentSession(id string) bool { return agentSessionRE.MatchString(id) }

// HookQuery is one hook run: the agent's chat ID (Claude Code's
// session_id), its folder, the event and, for Stop, whether the chat is
// already continuing because of a Stop hook.
type HookQuery struct {
	AgentSession   string
	Cwd            string
	Event          string
	StopHookActive bool
}

// HookAnswer is what the hook shows: Notice for the prompt (one line,
// local names only), and for Stop whether to keep the chat going.
type HookAnswer struct {
	Counts Counts
	Notice string
	Block  bool
	Reason string
}

// HookSessions is what HookService needs from shared sessions.
// Implemented by *SessionService.
type HookSessions interface {
	Get(ctx context.Context, id string) (store.SharedSession, error)
	List(ctx context.Context, states ...core.SessionState) ([]store.SharedSession, error)
}

// HookCounts is what HookService needs from AttentionService.
type HookCounts interface {
	Counts(ctx context.Context, sessionID string) (Counts, error)
	Listening(sessionID string) bool
}

// HookService answers the agent hooks for the shared session of the chat
// that runs them (v2 spec 3.2 and 7.1). The MCP server tells the daemon
// the agent's chat ID when it shares or reattaches, so two chats in one
// folder are told apart. Without that ID (an agent that does not give its
// MCP server one), the newest open session of the folder whose chat ID is
// unknown answers.
type HookService struct {
	sessions HookSessions
	counts   HookCounts

	mu    sync.Mutex
	chats map[string]string     // agent chat ID -> shared session ID
	stops map[string]*stopState // agent chat ID (or session ID) -> Stop hook state
}

type stopState struct {
	blocks int   // blocks in a row
	seq    int64 // newest unread item when it last blocked
}

// NewHookService builds the service.
func NewHookService(sessions HookSessions, counts HookCounts) *HookService {
	return &HookService{sessions: sessions, counts: counts, chats: map[string]string{}, stops: map[string]*stopState{}}
}

// Bind records that the agent chat agentSession holds shared session
// sessionID (session_share or a reattach). Invalid chat IDs are ignored.
func (h *HookService) Bind(agentSession, sessionID string) {
	if !ValidAgentSession(agentSession) || sessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.chats {
		if v == sessionID {
			delete(h.chats, k)
		}
	}
	h.chats[agentSession] = sessionID
}

// resolve returns the shared session the hook's chat holds.
func (h *HookService) resolve(ctx context.Context, q HookQuery) (store.SharedSession, bool) {
	h.mu.Lock()
	id, known := h.chats[q.AgentSession]
	bound := make(map[string]bool, len(h.chats))
	for _, v := range h.chats {
		bound[v] = true
	}
	h.mu.Unlock()
	if known {
		s, err := h.sessions.Get(ctx, id)
		if err != nil || s.State == core.SessionClosed {
			return store.SharedSession{}, false
		}
		return s, true
	}
	open, err := h.sessions.List(ctx, core.SessionOpen)
	if err != nil {
		return store.SharedSession{}, false
	}
	var found store.SharedSession
	ok := false
	for _, s := range open { // oldest first: the last match is the newest
		if s.ProjectDir == q.Cwd && q.Cwd != "" && !bound[s.ID] {
			found, ok = s, true
		}
	}
	return found, ok
}

// Check answers one hook run.
func (h *HookService) Check(ctx context.Context, q HookQuery) (HookAnswer, error) {
	s, ok := h.resolve(ctx, q)
	if !ok {
		return HookAnswer{}, nil
	}
	c, err := h.counts.Counts(ctx, s.ID)
	if err != nil {
		return HookAnswer{}, err
	}
	a := HookAnswer{Counts: c, Notice: present.PendingLine(c.Groups)}
	listening := h.counts.Listening(s.ID)
	switch q.Event {
	case HookStop, HookSubagentStop:
		a.Block = h.stop(q, s.ID, c)
		if a.Block {
			a.Reason = present.PendingLine(unhandledGroups(c.Groups))
			if !listening {
				a.Reason += " Then start the listener again."
			}
		}
	default:
		h.resetStops(q, s.ID)
		// Only Claude Code (UserPromptSubmit) runs the listener; other
		// agents (Codex notify) poll with wait_for_message.
		a.Notice = promptNotice(a.Notice, c, listening || q.Event != HookUserPromptSubmit)
	}
	return a, nil
}

// stop decides whether the Stop hook blocks: only for unhandled items
// (never for decisions alone), at most MaxStopBlocks times in a row, and
// while the chat is already continuing because of a Stop hook, only for
// items that arrived after the last block.
func (h *HookService) stop(q HookQuery, sessionID string, c Counts) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := stopKey(q, sessionID)
	st := h.stops[key]
	if st == nil || !q.StopHookActive {
		st = &stopState{}
		h.stops[key] = st
	}
	if c.Unhandled() == 0 || st.blocks >= MaxStopBlocks || q.StopHookActive && c.LastSeq <= st.seq {
		delete(h.stops, key)
		return false
	}
	st.blocks++
	st.seq = c.LastSeq
	return true
}

func (h *HookService) resetStops(q HookQuery, sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.stops, stopKey(q, sessionID))
}

// unhandledGroups drops the decision groups (the Stop hook's reason names
// only what check_inbox would return).
func unhandledGroups(gs []present.Pending) []present.Pending {
	var out []present.Pending
	for _, g := range gs {
		if !present.IsDecision(g.Kind) {
			out = append(out, g)
		}
	}
	return out
}

func stopKey(q HookQuery, sessionID string) string {
	if ValidAgentSession(q.AgentSession) {
		return "chat:" + q.AgentSession
	}
	return "session:" + sessionID
}

// promptNotice is the UserPromptSubmit line: what is new, decisions the
// agent already saw but the human has not made, and a reminder when no
// listener runs for the session.
func promptNotice(line string, c Counts, listening bool) string {
	if line == "" && c.Requests+c.Approvals > 0 {
		n := c.Requests + c.Approvals
		line = fmt.Sprintf("cravv-connect: %d %s for your human. Call review_pending.", n, pluralWord(n, "decision waits", "decisions wait"))
	}
	if !listening {
		if line == "" {
			line = "cravv-connect:"
		}
		line += " The listener for this chat's session is not running: start it again as a background command."
	}
	return line
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
