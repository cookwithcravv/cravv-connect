package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/store"
)

// PeerResolver turns "alias" or "alias/session" into a peer and session name.
// Implemented by *PeerService.
type PeerResolver interface {
	Resolve(ctx context.Context, addr string) (store.Peer, string, error)
}

// FileSender uploads a local file to a peer. Implemented by *FileService.
type FileSender interface {
	SendFile(ctx context.Context, to core.MachineID, projectDir, path, taskID string) (core.FileRef, error)
}

// CLIClaimMaxAge is how long a task claimed by a CLIAgent session may stay
// claimed or running before it fails as abandoned. CLI sessions are
// reclaimable for core.InboxRetention, so nothing else would end such a claim.
const CLIClaimMaxAge = 7 * 24 * time.Hour

// ApprovalPreviewChars is how much of a held task the approval list shows.
const ApprovalPreviewChars = 500

// Approval is one task awaiting human approval.
type Approval struct {
	Task    store.Task
	Alias   string
	Preview string // first ApprovalPreviewChars characters
	SHA256  string // hex of the full instructions
	Size    int    // bytes
}

// TaskDeps are the TaskService collaborators.
type TaskDeps struct {
	Tasks    store.TaskStore
	Peers    store.PeerStore
	Resolver PeerResolver
	Inbox    *InboxService
	Sender   EnvelopeSender
	Policy   TrustPolicy
	Files    FileSender
	Desktop  DesktopNotifier
	Clock    core.Clock
	Audit    audit.Logger
}

// TaskService runs the task state machine on both sides (spec 7.1, 8.2).
type TaskService struct{ d TaskDeps }

// NewTaskService builds a TaskService.
func NewTaskService(d TaskDeps) *TaskService {
	if d.Audit == nil {
		d.Audit = audit.Nop{}
	}
	return &TaskService{d: d}
}

var (
	activeInbound  = []core.TaskState{core.TaskAwaitingApproval, core.TaskQueued, core.TaskClaimed, core.TaskRunning}
	claimedStates  = []core.TaskState{core.TaskClaimed, core.TaskRunning}
	pendingStates  = []core.TaskState{core.TaskAwaitingApproval, core.TaskQueued}
	activeOutbound = []core.TaskState{core.TaskSent, core.TaskAwaitingApproval, core.TaskQueued, core.TaskClaimed, core.TaskRunning}
)

// mirrorRank orders sender-side states so a late update never moves a task backwards.
var mirrorRank = map[core.TaskState]int{
	core.TaskSent: 0, core.TaskAwaitingApproval: 1, core.TaskQueued: 2, core.TaskClaimed: 3, core.TaskRunning: 4,
	core.TaskDone: 5, core.TaskFailed: 5, core.TaskCancelled: 5, core.TaskRejected: 5, core.TaskExpired: 5,
}

// Create sends a task to "alias" or "alias/session" and records its sender-side mirror.
func (s *TaskService) Create(ctx context.Context, session, projectDir, to, instructions string, filePaths []string) (string, error) {
	if instructions == "" {
		return "", errors.New("instructions are empty")
	}
	if len(instructions) > core.MaxTextBytes {
		return "", fmt.Errorf("instructions: %w", core.ErrTooLarge)
	}
	peer, toSession, err := s.d.Resolver.Resolve(ctx, to)
	if err != nil {
		return "", err
	}
	if peer.Paused {
		return "", core.ErrPaused
	}
	taskID := core.NewID()
	files, err := s.sendFiles(ctx, peer.MachineID, projectDir, taskID, filePaths)
	if err != nil {
		return "", err
	}
	now := s.d.Clock.Now()
	t := store.Task{
		ID: taskID, Direction: store.TaskOutbound, Peer: peer.MachineID,
		FromSession: session, ToSession: toSession, Instructions: instructions,
		State: core.TaskSent, Files: files, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.d.Tasks.PutTask(ctx, t); err != nil {
		return "", err
	}
	body := core.TaskCreateBody{TaskID: taskID, Instructions: instructions, Files: files}
	if _, err := s.d.Sender.SendEnvelope(ctx, peer.MachineID, core.KindTaskCreate, session, toSession, body); err != nil {
		_, _ = s.d.Tasks.Transition(ctx, taskID, []core.TaskState{core.TaskSent}, func(t *store.Task) error {
			t.State = core.TaskFailed
			t.Notes = append(t.Notes, store.TaskNote{At: now, Text: "not sent: " + err.Error()})
			t.UpdatedAt = now
			return nil
		})
		return "", err
	}
	return taskID, nil
}

func (s *TaskService) sendFiles(ctx context.Context, to core.MachineID, projectDir, taskID string, paths []string) ([]core.FileRef, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if s.d.Files == nil {
		return nil, errors.New("file sending is not available")
	}
	refs := make([]core.FileRef, 0, len(paths))
	for _, p := range paths {
		ref, err := s.d.Files.SendFile(ctx, to, projectDir, p, taskID)
		if err != nil {
			return refs, fmt.Errorf("attach %s: %w", p, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// HandleCreate applies the trust policy to an incoming task.create. Behind a PolicyGate
// its own decision must match the gate's.
func (s *TaskService) HandleCreate(ctx context.Context, peer store.Peer, env core.Envelope) error {
	decision := s.d.Policy.Decide(peer.TrustIn, core.KindTaskCreate)
	return s.handleCreate(ctx, peer, env, decision)
}

// RejectCreate records an incoming task.create as rejected and tells the sender. It is
// the PolicyGate's OnReject for task.create.
func (s *TaskService) RejectCreate(ctx context.Context, peer store.Peer, env core.Envelope) error {
	return s.handleCreate(ctx, peer, env, DecisionReject)
}

func (s *TaskService) handleCreate(ctx context.Context, peer store.Peer, env core.Envelope, decision Decision) error {
	body, err := decodeEnvBody[core.TaskCreateBody](env.Body)
	if err != nil {
		return err
	}
	if err := checkPeerIDs("task.create", body.TaskID, body.Files); err != nil {
		return err
	}
	if err := checkGateDecision(ctx, decision, core.KindTaskCreate, body.TaskID); err != nil {
		return err
	}
	if len(body.Instructions) > core.MaxTextBytes {
		return fmt.Errorf("task %s: %w", body.TaskID, core.ErrTooLarge)
	}
	if cur, err := s.d.Tasks.GetTask(ctx, body.TaskID); err == nil {
		return Retryable(s.redeliverCreate(ctx, cur, peer, env.ID))
	} else if !errors.Is(err, core.ErrNotFound) {
		return Retryable(err)
	}
	now := s.d.Clock.Now()
	t := store.Task{
		ID: body.TaskID, Direction: store.TaskInbound, Peer: peer.MachineID,
		FromSession: env.FromSession, ToSession: env.ToSession, Instructions: body.Instructions,
		Files: body.Files, CreatedAt: now, UpdatedAt: now,
	}
	switch decision {
	case DecisionDeliver:
		t.State = core.TaskQueued
		t.ExpiresAt = now.Add(core.UnclaimedExpiry)
	case DecisionHold:
		t.State = core.TaskAwaitingApproval
		t.ExpiresAt = now.Add(core.ApprovalExpiry)
	default:
		t.State = core.TaskRejected
	}
	if err := s.d.Tasks.PutTask(ctx, t); err != nil {
		return Retryable(err)
	}
	_ = s.d.Audit.Record(audit.Event{
		Type: audit.EvTaskIn, Peer: peer.MachineID, Alias: peer.Alias, ItemID: t.ID,
		Hash: contentHash([]byte(t.Instructions)), Detail: map[string]any{"state": string(t.State)},
	})
	switch t.State {
	case core.TaskQueued:
		return Retryable(s.deliverTask(ctx, t, env.ID))
	case core.TaskAwaitingApproval:
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskAwaitingApproval})
		if s.d.Desktop != nil {
			s.d.Desktop.Notify("cravv-connect", fmt.Sprintf("cravv-connect: 1 task awaiting approval from %s", peer.Alias))
		}
	default:
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskRejected, Note: "not permitted"})
	}
	return nil
}

// redeliverCreate handles a task.create whose task already exists. When it is
// a redelivery of the message that created a queued task and the earlier
// attempt failed after storing the task, the inbox item is written now.
// Anything else (a duplicate, or an ID naming another task) is ignored.
func (s *TaskService) redeliverCreate(ctx context.Context, cur store.Task, peer store.Peer, msgID string) error {
	if cur.Direction != store.TaskInbound || cur.Peer != peer.MachineID || cur.State != core.TaskQueued {
		return nil
	}
	done, err := s.d.Inbox.Delivered(ctx, msgID)
	if err != nil || done {
		return err
	}
	return s.deliverTask(ctx, cur, msgID)
}

func (s *TaskService) deliverTask(ctx context.Context, t store.Task, msgID string) error {
	body, err := json.Marshal(core.TaskCreateBody{TaskID: t.ID, Instructions: t.Instructions, Files: t.Files})
	if err != nil {
		return err
	}
	_, err = s.d.Inbox.Deliver(ctx, store.InboxItem{
		MsgID: msgID, From: t.Peer, FromSession: t.FromSession, ToSession: t.ToSession,
		Kind: core.KindTaskCreate, Body: body, TaskID: t.ID,
	})
	return err
}

// sendUpdate reports an inbound task's state to its sender. It is best
// effort: the outbox persists it, and it only fails when the peer is gone.
func (s *TaskService) sendUpdate(ctx context.Context, t store.Task, body core.TaskUpdateBody) {
	_, _ = s.d.Sender.SendEnvelope(ctx, t.Peer, core.KindTaskUpdate, t.ClaimedBy, t.FromSession, body)
}

func (s *TaskService) inboundTask(ctx context.Context, id string) (store.Task, error) {
	t, err := s.d.Tasks.GetTask(ctx, id)
	if err != nil {
		return t, err
	}
	if t.Direction != store.TaskInbound {
		return t, core.ErrNotFound
	}
	return t, nil
}

// Claim atomically gives a queued task to one session.
func (s *TaskService) Claim(ctx context.Context, session, id string) (store.Task, error) {
	if _, err := s.inboundTask(ctx, id); err != nil {
		return store.Task{}, err
	}
	now := s.d.Clock.Now()
	t, err := s.d.Tasks.Transition(ctx, id, []core.TaskState{core.TaskQueued}, func(t *store.Task) error {
		t.State = core.TaskClaimed
		t.ClaimedBy = session
		t.ExpiresAt = time.Time{}
		if isCLISession(session) {
			t.ExpiresAt = now.Add(CLIClaimMaxAge)
		}
		t.UpdatedAt = now
		return nil
	})
	if errors.Is(err, core.ErrBadTransition) {
		cur, gerr := s.d.Tasks.GetTask(ctx, id)
		if gerr != nil {
			return cur, gerr
		}
		if cur.State == core.TaskClaimed || cur.State == core.TaskRunning {
			if cur.ClaimedBy == session {
				return cur, nil
			}
			return cur, core.ErrAlreadyClaimed
		}
		return cur, err
	}
	if err != nil {
		return t, err
	}
	s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskClaimed})
	return t, nil
}

func (s *TaskService) claimerTransition(ctx context.Context, session, id string, mutate func(t *store.Task)) (store.Task, error) {
	if _, err := s.inboundTask(ctx, id); err != nil {
		return store.Task{}, err
	}
	now := s.d.Clock.Now()
	return s.d.Tasks.Transition(ctx, id, claimedStates, func(t *store.Task) error {
		if t.ClaimedBy != session {
			return core.ErrNotPermitted
		}
		mutate(t)
		t.UpdatedAt = now
		return nil
	})
}

// Update adds a progress note; the first note moves claimed to running.
func (s *TaskService) Update(ctx context.Context, session, id, note string) (store.Task, error) {
	if len(note) > core.MaxTextBytes {
		return store.Task{}, fmt.Errorf("note: %w", core.ErrTooLarge)
	}
	now := s.d.Clock.Now()
	t, err := s.claimerTransition(ctx, session, id, func(t *store.Task) {
		t.State = core.TaskRunning
		t.Notes = append(t.Notes, store.TaskNote{At: now, Text: note})
	})
	if err != nil {
		return t, err
	}
	s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskRunning, Note: note})
	return t, nil
}

// Complete finishes a claimed task with a result and optional files. The task
// moves to done first, so files are only uploaded for a task this session
// really finished (a cancel racing the upload finds it done). A file that
// cannot be sent is reported in the update's note and returned as an error;
// the task stays done with the files that were sent.
func (s *TaskService) Complete(ctx context.Context, session, projectDir, id, result string, filePaths []string) (store.Task, error) {
	if len(result) > core.MaxTextBytes {
		return store.Task{}, fmt.Errorf("result: %w", core.ErrTooLarge)
	}
	t, err := s.claimerTransition(ctx, session, id, func(t *store.Task) {
		t.State = core.TaskDone
		t.Result = result
	})
	if err != nil {
		return t, err
	}
	files, ferr := s.sendFiles(ctx, t.Peer, projectDir, id, filePaths)
	if len(files) > 0 {
		t, err = s.d.Tasks.Transition(ctx, id, []core.TaskState{core.TaskDone}, func(t *store.Task) error {
			t.ResultFiles = files
			return nil
		})
		if err != nil {
			return t, err
		}
	}
	upd := core.TaskUpdateBody{TaskID: t.ID, State: core.TaskDone, Result: result, Files: files}
	if ferr != nil {
		upd.Note = "some result files were not sent: " + ferr.Error()
	}
	s.sendUpdate(ctx, t, upd)
	return t, ferr
}

// Fail ends a claimed task with a reason.
func (s *TaskService) Fail(ctx context.Context, session, id, reason string) (store.Task, error) {
	if len(reason) > core.MaxTextBytes {
		return store.Task{}, fmt.Errorf("reason: %w", core.ErrTooLarge)
	}
	now := s.d.Clock.Now()
	t, err := s.claimerTransition(ctx, session, id, func(t *store.Task) {
		t.State = core.TaskFailed
		t.Notes = append(t.Notes, store.TaskNote{At: now, Text: reason})
	})
	if err != nil {
		return t, err
	}
	s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskFailed, Note: reason})
	return t, nil
}

// Cancel is sender-side only: the session that created the task cancels it.
func (s *TaskService) Cancel(ctx context.Context, session, id string) (store.Task, error) {
	cur, err := s.d.Tasks.GetTask(ctx, id)
	if err != nil {
		return cur, err
	}
	if cur.Direction != store.TaskOutbound || cur.FromSession != session {
		return store.Task{}, core.ErrNotFound
	}
	now := s.d.Clock.Now()
	t, err := s.d.Tasks.Transition(ctx, id, activeOutbound, func(t *store.Task) error {
		t.State = core.TaskCancelled
		t.UpdatedAt = now
		return nil
	})
	if err != nil {
		return t, err
	}
	_, err = s.d.Sender.SendEnvelope(ctx, t.Peer, core.KindTaskCancel, session, t.ToSession, core.TaskCancelBody{TaskID: id})
	return t, err
}

// HandleCancel cancels an inbound task when its sender asks and tells the claimer.
func (s *TaskService) HandleCancel(ctx context.Context, peer store.Peer, env core.Envelope) error {
	body, err := decodeEnvBody[core.TaskCancelBody](env.Body)
	if err != nil {
		return err
	}
	if err := checkPeerIDs("task.cancel", body.TaskID, nil); err != nil {
		return err
	}
	cur, err := s.d.Tasks.GetTask(ctx, body.TaskID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return Retryable(err)
	}
	if cur.Direction != store.TaskInbound || cur.Peer != peer.MachineID {
		return nil // a peer may only cancel its own tasks
	}
	now := s.d.Clock.Now()
	t, err := s.d.Tasks.Transition(ctx, body.TaskID, activeInbound, func(t *store.Task) error {
		t.State = core.TaskCancelled
		t.Notes = append(t.Notes, store.TaskNote{At: now, Text: "cancelled by sender", MsgID: env.ID})
		t.UpdatedAt = now
		return nil
	})
	if errors.Is(err, core.ErrBadTransition) {
		// Already finished, or cancelled by this message on an earlier attempt
		// that failed before the claimer was told: tell it now.
		t, err = s.d.Tasks.GetTask(ctx, body.TaskID)
		if err != nil {
			return Retryable(err)
		}
		if t.State != core.TaskCancelled || !hasNoteFrom(t, env.ID) {
			return nil
		}
		if done, err := s.d.Inbox.Delivered(ctx, env.ID); err != nil || done {
			return Retryable(err)
		}
	} else if err != nil {
		return Retryable(err)
	}
	notice, err := json.Marshal(core.TaskUpdateBody{TaskID: t.ID, State: core.TaskCancelled, Note: "cancelled by sender"})
	if err != nil {
		return err
	}
	_, err = s.d.Inbox.Deliver(ctx, store.InboxItem{
		MsgID: env.ID, From: peer.MachineID, FromSession: env.FromSession, ToSession: t.ClaimedBy,
		Kind: core.KindTaskUpdate, Body: notice, TaskID: t.ID,
	})
	return Retryable(err)
}

// HandleUpdate updates the sender-side mirror and tells the creating session.
func (s *TaskService) HandleUpdate(ctx context.Context, peer store.Peer, env core.Envelope) error {
	body, err := decodeEnvBody[core.TaskUpdateBody](env.Body)
	if err != nil {
		return err
	}
	if err := checkPeerIDs("task.update", body.TaskID, body.Files); err != nil {
		return err
	}
	if _, known := mirrorRank[body.State]; !known || body.State == core.TaskSent {
		return fmt.Errorf("task.update with state %q", body.State)
	}
	if len(body.Note) > core.MaxTextBytes || len(body.Result) > core.MaxTextBytes {
		return fmt.Errorf("task.update %s: %w", body.TaskID, core.ErrTooLarge)
	}
	cur, err := s.d.Tasks.GetTask(ctx, body.TaskID)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return Retryable(err)
	}
	if cur.Direction != store.TaskOutbound || cur.Peer != peer.MachineID {
		return nil // only the task's receiver may update it
	}
	// A redelivery after an attempt that stored the update but failed to tell
	// the session: the inbox item is the last step, so its presence means done.
	if done, err := s.d.Inbox.Delivered(ctx, env.ID); err != nil {
		return Retryable(err)
	} else if done {
		return nil
	}
	now := s.d.Clock.Now()
	t, err := s.d.Tasks.Transition(ctx, body.TaskID, activeOutbound, func(t *store.Task) error {
		if mirrorRank[body.State] >= mirrorRank[t.State] {
			t.State = body.State
		}
		if body.Note != "" && !hasNoteFrom(*t, env.ID) {
			t.Notes = append(t.Notes, store.TaskNote{At: now, Text: body.Note, MsgID: env.ID})
		}
		if body.Result != "" {
			t.Result = body.Result
		}
		if len(body.Files) > 0 {
			t.ResultFiles = body.Files
		}
		if body.State == core.TaskClaimed || body.State == core.TaskRunning {
			t.ClaimedBy = env.FromSession
		}
		t.UpdatedAt = now
		return nil
	})
	if errors.Is(err, core.ErrBadTransition) {
		// Already terminal here. Either another update or a local cancel ended
		// it (ignore this one), or this very update did on an earlier attempt
		// that failed before the session was told (tell it now).
		t, err = s.d.Tasks.GetTask(ctx, body.TaskID)
		if err != nil {
			return Retryable(err)
		}
		if t.State != body.State {
			return nil
		}
	} else if err != nil {
		return Retryable(err)
	}
	_, err = s.d.Inbox.Deliver(ctx, store.InboxItem{
		MsgID: env.ID, From: peer.MachineID, FromSession: env.FromSession, ToSession: t.FromSession,
		Kind: core.KindTaskUpdate, Body: env.Body, TaskID: t.ID,
	})
	return Retryable(err)
}

// Get returns a task a session may see: an inbound task a human did not hold
// or reject, or an outbound task the session created. Held (awaiting_approval)
// and rejected inbound tasks look like they do not exist, so agents never read
// instructions no human approved.
func (s *TaskService) Get(ctx context.Context, session, id string) (store.Task, error) {
	t, err := s.d.Tasks.GetTask(ctx, id)
	if err != nil {
		return store.Task{}, err
	}
	if t.Direction == store.TaskOutbound && t.FromSession != session {
		return store.Task{}, core.ErrNotFound
	}
	if t.Direction == store.TaskInbound && (t.State == core.TaskAwaitingApproval || t.State == core.TaskRejected) {
		return store.Task{}, core.ErrNotFound
	}
	return t, nil
}

// Approvals lists tasks awaiting human approval, oldest first.
func (s *TaskService) Approvals(ctx context.Context) ([]Approval, error) {
	ts, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: []core.TaskState{core.TaskAwaitingApproval}})
	if err != nil {
		return nil, err
	}
	out := make([]Approval, 0, len(ts))
	for _, t := range ts {
		out = append(out, Approval{
			Task:    t,
			Alias:   s.alias(ctx, t.Peer),
			Preview: previewText(t.Instructions, ApprovalPreviewChars),
			SHA256:  contentHash([]byte(t.Instructions)),
			Size:    len(t.Instructions),
		})
	}
	return out, nil
}

// PendingApprovals counts tasks awaiting approval.
func (s *TaskService) PendingApprovals(ctx context.Context) (int, error) {
	ts, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: []core.TaskState{core.TaskAwaitingApproval}})
	return len(ts), err
}

// Decide approves (queued, delivered to sessions) or denies (rejected) a held task.
// It is a human-only action: unlocked must be true (the IPC layer sets it after
// auth.unlock), otherwise it fails with core.ErrAuthRequired.
func (s *TaskService) Decide(ctx context.Context, id string, approve, unlocked bool) error {
	if !unlocked {
		return core.ErrAuthRequired
	}
	cur, err := s.inboundTask(ctx, id)
	if err != nil {
		return err
	}
	if approve {
		if err := s.checkPeerForApproval(ctx, cur.Peer); err != nil {
			return err
		}
	}
	now := s.d.Clock.Now()
	t, err := s.d.Tasks.Transition(ctx, id, []core.TaskState{core.TaskAwaitingApproval}, func(t *store.Task) error {
		if approve {
			t.State = core.TaskQueued
			t.ExpiresAt = now.Add(core.UnclaimedExpiry)
		} else {
			t.State = core.TaskRejected
			t.ExpiresAt = time.Time{}
		}
		t.UpdatedAt = now
		return nil
	})
	if err != nil {
		return err
	}
	ev := audit.Event{Type: audit.EvDeny, Peer: t.Peer, Alias: s.alias(ctx, t.Peer), ItemID: t.ID, Hash: contentHash([]byte(t.Instructions))}
	if approve {
		ev.Type = audit.EvApprove
	}
	_ = s.d.Audit.Record(ev)
	if !approve {
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskRejected, Note: "denied by the receiving human"})
		return nil
	}
	s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskQueued})
	return s.deliverTask(ctx, t, t.ID)
}

// checkPeerForApproval refuses an approval when the peer is no longer paired,
// is paused by us, or its trust level no longer allows tasks.
func (s *TaskService) checkPeerForApproval(ctx context.Context, id core.MachineID) error {
	p, err := s.d.Peers.GetPeer(ctx, id)
	if err != nil {
		return fmt.Errorf("peer %s: %w", id.Short(), err)
	}
	if p.Paused {
		return fmt.Errorf("%s: %w", p.Alias, core.ErrPaused)
	}
	if s.d.Policy.Decide(p.TrustIn, core.KindTaskCreate) == DecisionReject {
		return fmt.Errorf("%s is %s: %w", p.Alias, p.TrustIn, core.ErrNotPermitted)
	}
	return nil
}

// PeerCutOff implements PeerCutOffObserver: the peer's tasks awaiting approval
// are rejected and the sender is told (best effort).
func (s *TaskService) PeerCutOff(ctx context.Context, peer store.Peer, reason string) error {
	ts, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound,
		States: []core.TaskState{core.TaskAwaitingApproval}, Peer: peer.MachineID})
	if err != nil {
		return err
	}
	now := s.d.Clock.Now()
	for _, cand := range ts {
		t, err := s.d.Tasks.Transition(ctx, cand.ID, []core.TaskState{core.TaskAwaitingApproval}, func(t *store.Task) error {
			t.State = core.TaskRejected
			t.ExpiresAt = time.Time{}
			t.Notes = append(t.Notes, store.TaskNote{At: now, Text: reason})
			t.UpdatedAt = now
			return nil
		})
		if errors.Is(err, core.ErrBadTransition) {
			continue
		}
		if err != nil {
			return err
		}
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskRejected, Note: reason})
	}
	return nil
}

// ExpireDue expires held and unclaimed tasks past ExpiresAt and tells senders.
func (s *TaskService) ExpireDue(ctx context.Context) (int, error) {
	now := s.d.Clock.Now()
	ts, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: pendingStates, ExpiredBefore: now})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, cand := range ts {
		t, err := s.d.Tasks.Transition(ctx, cand.ID, pendingStates, func(t *store.Task) error {
			t.State = core.TaskExpired
			t.UpdatedAt = now
			return nil
		})
		if errors.Is(err, core.ErrBadTransition) {
			continue // claimed or decided meanwhile
		}
		if err != nil {
			return n, err
		}
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskExpired})
		n++
	}
	return n, nil
}

// AbandonSession fails the tasks an expired session had claimed.
func (s *TaskService) AbandonSession(ctx context.Context, name string) error {
	return s.failClaimed(ctx, store.TaskFilter{Direction: store.TaskInbound, States: claimedStates, ClaimedBy: name}, "abandoned")
}

// AbandonStaleCLIClaims fails tasks a CLIAgent session claimed more than
// CLIClaimMaxAge ago ("abandoned") and tells their senders.
func (s *TaskService) AbandonStaleCLIClaims(ctx context.Context) (int, error) {
	ts, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: claimedStates, ExpiredBefore: s.d.Clock.Now()})
	if err != nil {
		return 0, err
	}
	stale := ts[:0]
	for _, t := range ts {
		if isCLISession(t.ClaimedBy) {
			stale = append(stale, t)
		}
	}
	return s.failTasks(ctx, stale, "abandoned")
}

// isCLISession reports whether a session name belongs to the --json CLI
// (sessions are named <agent>@<dir>).
func isCLISession(name string) bool { return strings.HasPrefix(name, CLIAgent+"@") }

// FailActive fails every claimed or running inbound task (kill switch).
func (s *TaskService) FailActive(ctx context.Context, reason string) error {
	return s.failClaimed(ctx, store.TaskFilter{Direction: store.TaskInbound, States: claimedStates}, reason)
}

func (s *TaskService) failClaimed(ctx context.Context, f store.TaskFilter, reason string) error {
	ts, err := s.d.Tasks.ListTasks(ctx, f)
	if err != nil {
		return err
	}
	_, err = s.failTasks(ctx, ts, reason)
	return err
}

// failTasks fails the listed tasks that are still claimed or running and tells their senders.
func (s *TaskService) failTasks(ctx context.Context, ts []store.Task, reason string) (int, error) {
	now := s.d.Clock.Now()
	n := 0
	for _, cand := range ts {
		t, err := s.d.Tasks.Transition(ctx, cand.ID, claimedStates, func(t *store.Task) error {
			t.State = core.TaskFailed
			t.ExpiresAt = time.Time{}
			t.Notes = append(t.Notes, store.TaskNote{At: now, Text: reason})
			t.UpdatedAt = now
			return nil
		})
		if errors.Is(err, core.ErrBadTransition) {
			continue
		}
		if err != nil {
			return n, err
		}
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskFailed, Note: reason})
		n++
	}
	return n, nil
}

// RecheckPeer re-applies the trust policy to a peer's pending tasks after its
// trust level changed: a peer lowered to chat-only has them rejected.
func (s *TaskService) RecheckPeer(ctx context.Context, peer core.MachineID, level core.TrustLevel) error {
	if s.d.Policy.Decide(level, core.KindTaskCreate) != DecisionReject {
		return nil
	}
	ts, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: pendingStates, Peer: peer})
	if err != nil {
		return err
	}
	now := s.d.Clock.Now()
	for _, cand := range ts {
		t, err := s.d.Tasks.Transition(ctx, cand.ID, pendingStates, func(t *store.Task) error {
			t.State = core.TaskRejected
			t.UpdatedAt = now
			return nil
		})
		if errors.Is(err, core.ErrBadTransition) {
			continue
		}
		if err != nil {
			return err
		}
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskRejected, Note: "not permitted"})
	}
	return nil
}

// TrustLowered implements TrustObserver: PeerService calls it after lowering
// a peer's trust, with the peer record already carrying the new level.
func (s *TaskService) TrustLowered(ctx context.Context, peer store.Peer) error {
	return s.RecheckPeer(ctx, peer.MachineID, peer.TrustIn)
}

// hasNoteFrom reports whether t already has a note from message msgID.
func hasNoteFrom(t store.Task, msgID string) bool {
	for _, n := range t.Notes {
		if n.MsgID == msgID {
			return true
		}
	}
	return false
}

func (s *TaskService) alias(ctx context.Context, id core.MachineID) string {
	if p, err := s.d.Peers.GetPeer(ctx, id); err == nil {
		return p.Alias
	}
	return id.Short()
}

func previewText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func contentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// errBadPeerID rejects a message carrying an ID that core.NewID could not have
// produced. It is not retryable: the message is dropped and acknowledged.
var errBadPeerID = errors.New("peer-supplied id is not a valid ID")

// checkPeerIDs validates the task ID and file IDs a peer sent. Peer IDs are
// shown in terminals and agent prompts, so only the exact core ID format is
// accepted.
func checkPeerIDs(kind, taskID string, files []core.FileRef) error {
	if !core.ValidID(taskID) {
		return fmt.Errorf("%s: task_id %q: %w", kind, taskID, errBadPeerID)
	}
	for _, f := range files {
		if !core.ValidID(f.FileID) {
			return fmt.Errorf("%s %s: file_id %q: %w", kind, taskID, f.FileID, errBadPeerID)
		}
	}
	return nil
}
