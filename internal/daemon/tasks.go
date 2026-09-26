package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// FileSender uploads a local file over a link. Implemented by *FileService.
type FileSender interface {
	SendFile(ctx context.Context, l store.Link, projectDir, path, taskID string) (core.FileRef, error)
}

// ActiveLinks returns a session's active link by number (core.ErrLinkClosed
// when it is not active). Implemented by *LinkService.
type ActiveLinks interface {
	Active(ctx context.Context, sessionID string, num int64) (store.Link, error)
}

// ReasonLinkClosed is the failure reason of a task whose link closed.
const ReasonLinkClosed = "link_closed"

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
	Tasks   store.TaskStore
	Peers   store.PeerStore
	Links   ActiveLinks
	Lookup  LinkLookup
	Inbox   *InboxService
	Sender  EnvelopeSender
	Policy  PermissionPolicy
	Files   FileSender
	Desktop DesktopNotifier
	Clock   core.Clock
	Audit   audit.Logger
}

// TaskService runs the task state machine on both sides of a link. A task
// belongs to one link and to the link's local session on each side.
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
	activeOutbound = []core.TaskState{core.TaskSent, core.TaskAwaitingApproval, core.TaskQueued, core.TaskSeen, core.TaskClaimed, core.TaskRunning}
)

// mirrorRank orders sender-side states so a late update never moves a task backwards.
var mirrorRank = map[core.TaskState]int{
	core.TaskSent: 0, core.TaskAwaitingApproval: 1, core.TaskQueued: 2, core.TaskSeen: 3, core.TaskClaimed: 4, core.TaskRunning: 5,
	core.TaskDone: 6, core.TaskFailed: 6, core.TaskCancelled: 6, core.TaskRejected: 6, core.TaskExpired: 6,
}

// Create sends a task over the session's active link num and records its
// sender-side mirror. A link on which the peer allows messages only refuses
// it at once (core.ErrNotPermitted).
func (s *TaskService) Create(ctx context.Context, session, projectDir string, link int64, instructions string, filePaths []string) (string, error) {
	if instructions == "" {
		return "", errors.New("instructions are empty")
	}
	if len(instructions) > core.MaxTextBytes {
		return "", fmt.Errorf("instructions: %w", core.ErrTooLarge)
	}
	l, err := s.d.Links.Active(ctx, session, link)
	if err != nil {
		return "", err
	}
	if l.PermissionOut == core.PermMessages {
		return "", fmt.Errorf("link %d allows messages only: %w", link, core.ErrNotPermitted)
	}
	taskID := core.NewID()
	files, err := s.sendFiles(ctx, l, projectDir, taskID, filePaths)
	if err != nil {
		return "", err
	}
	now := s.d.Clock.Now()
	t := store.Task{
		ID: taskID, Direction: store.TaskOutbound, Peer: l.Peer, LinkID: l.ID,
		FromSession: session, ToSession: l.RemoteName, Instructions: instructions,
		State: core.TaskSent, Files: files, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.d.Tasks.PutTask(ctx, t); err != nil {
		return "", err
	}
	body := core.TaskCreateBody{TaskID: taskID, Instructions: instructions, Files: files}
	if _, err := s.d.Sender.SendEnvelope(ctx, l.Peer, core.KindTaskCreate, l.ID, body); err != nil {
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

func (s *TaskService) sendFiles(ctx context.Context, l store.Link, projectDir, taskID string, paths []string) ([]core.FileRef, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if s.d.Files == nil {
		return nil, errors.New("file sending is not available")
	}
	refs := make([]core.FileRef, 0, len(paths))
	for _, p := range paths {
		ref, err := s.d.Files.SendFile(ctx, l, projectDir, p, taskID)
		if err != nil {
			return refs, fmt.Errorf("attach %s: %w", p, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// HandleCreate applies the link's permission to an incoming task.create.
// Behind a LinkGate its own decision must match the gate's.
func (s *TaskService) HandleCreate(ctx context.Context, peer store.Peer, env core.Envelope) error {
	l, ok := LinkFrom(ctx)
	if !ok {
		return errNoLink
	}
	return s.handleCreate(ctx, peer, l, env, s.d.Policy.Decide(l.PermissionIn, core.KindTaskCreate))
}

// RejectCreate records an incoming task.create as rejected and tells the
// sender. It is the LinkGate's OnReject for task.create.
func (s *TaskService) RejectCreate(ctx context.Context, peer store.Peer, env core.Envelope) error {
	l, ok := LinkFrom(ctx)
	if !ok {
		return errNoLink
	}
	return s.handleCreate(ctx, peer, l, env, DecisionReject)
}

func (s *TaskService) handleCreate(ctx context.Context, peer store.Peer, l store.Link, env core.Envelope, decision Decision) error {
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
		return Retryable(s.redeliverCreate(ctx, cur, l, env.ID))
	} else if !errors.Is(err, core.ErrNotFound) {
		return Retryable(err)
	}
	now := s.d.Clock.Now()
	t := store.Task{
		ID: body.TaskID, Direction: store.TaskInbound, Peer: peer.MachineID, LinkID: l.ID,
		FromSession: l.RemoteName, ToSession: l.Session, Instructions: body.Instructions,
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
func (s *TaskService) redeliverCreate(ctx context.Context, cur store.Task, l store.Link, msgID string) error {
	if cur.Direction != store.TaskInbound || cur.Peer != l.Peer || cur.LinkID != l.ID || cur.State != core.TaskQueued {
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
		MsgID: msgID, From: t.Peer, FromSession: t.FromSession, ToSession: t.ToSession, LinkID: t.LinkID,
		Kind: core.KindTaskCreate, Body: body, TaskID: t.ID,
	})
	return err
}

// ItemsRead implements ReadObserver: the first time the receiving session's
// inbox returns a queued task, its sender is told it was seen (v2 spec 4),
// so a slow session and a stuck one look different. The task stays queued.
func (s *TaskService) ItemsRead(ctx context.Context, session string, items []store.InboxItem) {
	for _, it := range items {
		if it.Kind != core.KindTaskCreate || it.TaskID == "" {
			continue
		}
		t, err := s.inboundTask(ctx, session, it.TaskID)
		if err != nil || t.State != core.TaskQueued {
			continue
		}
		s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskSeen})
	}
}

// sendUpdate reports an inbound task's state to its sender over the task's
// link. It is best effort: the outbox persists it, and after the link
// closed the sender fails the task on its own side.
func (s *TaskService) sendUpdate(ctx context.Context, t store.Task, body core.TaskUpdateBody) {
	_, _ = s.d.Sender.SendEnvelope(ctx, t.Peer, core.KindTaskUpdate, t.LinkID, body)
}

// inboundTask returns an inbound task of the session; any other task looks missing.
func (s *TaskService) inboundTask(ctx context.Context, session, id string) (store.Task, error) {
	t, err := s.d.Tasks.GetTask(ctx, id)
	if err != nil {
		return t, err
	}
	if t.Direction != store.TaskInbound || t.ToSession != session {
		return store.Task{}, core.ErrNotFound
	}
	return t, nil
}

// Claim gives a queued task to the session it was sent to. Only that
// session sees it, but claiming stays atomic and explicit.
func (s *TaskService) Claim(ctx context.Context, session, id string) (store.Task, error) {
	if _, err := s.inboundTask(ctx, session, id); err != nil {
		return store.Task{}, err
	}
	now := s.d.Clock.Now()
	t, err := s.d.Tasks.Transition(ctx, id, []core.TaskState{core.TaskQueued}, func(t *store.Task) error {
		t.State = core.TaskClaimed
		t.ClaimedBy = session
		t.ExpiresAt = time.Time{}
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
	if _, err := s.inboundTask(ctx, session, id); err != nil {
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
	var files []core.FileRef
	var ferr error
	if len(filePaths) > 0 {
		var l store.Link
		if l, ferr = s.d.Lookup.GetLink(ctx, t.Peer, t.LinkID); ferr == nil {
			if l.State != store.LinkActive {
				ferr = fmt.Errorf("link %d: %w", l.Num, core.ErrLinkClosed)
			} else {
				files, ferr = s.sendFiles(ctx, l, projectDir, id, filePaths)
			}
		}
	}
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
	_, err = s.d.Sender.SendEnvelope(ctx, t.Peer, core.KindTaskCancel, t.LinkID, core.TaskCancelBody{TaskID: id})
	return t, err
}

// HandleCancel cancels an inbound task when its sender asks and tells the session.
func (s *TaskService) HandleCancel(ctx context.Context, peer store.Peer, env core.Envelope) error {
	l, ok := LinkFrom(ctx)
	if !ok {
		return errNoLink
	}
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
	if cur.Direction != store.TaskInbound || cur.Peer != peer.MachineID || cur.LinkID != l.ID {
		return nil // a peer may only cancel its own tasks, on their own link
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
		MsgID: env.ID, From: peer.MachineID, FromSession: l.RemoteName, ToSession: t.ToSession, LinkID: t.LinkID,
		Kind: core.KindTaskUpdate, Body: notice, TaskID: t.ID,
	})
	return Retryable(err)
}

// HandleUpdate updates the sender-side mirror and tells the creating session.
func (s *TaskService) HandleUpdate(ctx context.Context, peer store.Peer, env core.Envelope) error {
	l, ok := LinkFrom(ctx)
	if !ok {
		return errNoLink
	}
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
	if cur.Direction != store.TaskOutbound || cur.Peer != peer.MachineID || cur.LinkID != l.ID {
		return nil // only the task's receiver may update it, on the task's link
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
			t.ClaimedBy = l.RemoteName
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
		MsgID: env.ID, From: peer.MachineID, FromSession: l.RemoteName, ToSession: t.FromSession, LinkID: t.LinkID,
		Kind: core.KindTaskUpdate, Body: env.Body, TaskID: t.ID,
	})
	return Retryable(err)
}

// Get returns a task the session may see: an inbound task sent to it that a
// human did not hold or reject, or an outbound task it created. Every other
// task, including another session's, looks like it does not exist, so
// agents never read instructions no human approved or another session's work.
func (s *TaskService) Get(ctx context.Context, session, id string) (store.Task, error) {
	t, err := s.d.Tasks.GetTask(ctx, id)
	if err != nil {
		return store.Task{}, err
	}
	if t.Direction == store.TaskOutbound && t.FromSession != session {
		return store.Task{}, core.ErrNotFound
	}
	if t.Direction == store.TaskInbound && (t.ToSession != session || t.State == core.TaskAwaitingApproval || t.State == core.TaskRejected) {
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

// Decide approves (queued, delivered to the session) or denies (rejected) a
// held task. Approving is a human decision: it needs AuthChat (Phase 2) or
// AuthPassword (the CLI), otherwise core.ErrAuthRequired. Denying needs none.
func (s *TaskService) Decide(ctx context.Context, id string, approve bool, auth Authority) error {
	if approve && auth < AuthChat {
		return core.ErrAuthRequired
	}
	cur, err := s.d.Tasks.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if cur.Direction != store.TaskInbound {
		return core.ErrNotFound
	}
	if approve {
		if err := s.checkLinkForApproval(ctx, cur); err != nil {
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

// checkLinkForApproval refuses an approval when the task's link is no
// longer active or no longer allows tasks.
func (s *TaskService) checkLinkForApproval(ctx context.Context, t store.Task) error {
	l, err := s.d.Lookup.GetLink(ctx, t.Peer, t.LinkID)
	if err != nil || l.State != store.LinkActive {
		return fmt.Errorf("task %s: %w", t.ID, core.ErrLinkClosed)
	}
	if s.d.Policy.Decide(l.PermissionIn, core.KindTaskCreate) == DecisionReject {
		return fmt.Errorf("link %d allows %s: %w", l.Num, l.PermissionIn, core.ErrNotPermitted)
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
	failed, err := s.failTasksFrom(ctx, ts, claimedStates, reason, true)
	return len(failed), err
}

// failTasksFrom fails the listed tasks still in one of from and returns
// them; tell sends the update to the peer (inbound tasks only).
func (s *TaskService) failTasksFrom(ctx context.Context, ts []store.Task, from []core.TaskState, reason string, tell bool) ([]store.Task, error) {
	now := s.d.Clock.Now()
	var failed []store.Task
	for _, cand := range ts {
		t, err := s.d.Tasks.Transition(ctx, cand.ID, from, func(t *store.Task) error {
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
			return failed, err
		}
		if tell {
			s.sendUpdate(ctx, t, core.TaskUpdateBody{TaskID: t.ID, State: core.TaskFailed, Note: reason})
		}
		failed = append(failed, t)
	}
	return failed, nil
}

// LinkClosed implements LinkCloseObserver: every unfinished task on the link
// fails with link_closed on this side. The sender of an inbound task is told
// (best effort: it also fails the task itself when its side of the link closes).
func (s *TaskService) LinkClosed(ctx context.Context, l store.Link) error {
	in, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: activeInbound, LinkID: l.ID, Peer: l.Peer})
	if err != nil {
		return err
	}
	if _, err := s.failTasksFrom(ctx, in, activeInbound, ReasonLinkClosed, true); err != nil {
		return err
	}
	out, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskOutbound, States: activeOutbound, LinkID: l.ID, Peer: l.Peer})
	if err != nil {
		return err
	}
	failed, err := s.failTasksFrom(ctx, out, activeOutbound, ReasonLinkClosed, false)
	var errs []error
	for _, t := range failed { // the session that sent it is waiting for an update
		body, _ := json.Marshal(core.TaskUpdateBody{TaskID: t.ID, State: core.TaskFailed, Note: ReasonLinkClosed})
		_, derr := s.d.Inbox.Deliver(ctx, store.InboxItem{
			MsgID: core.NewIDAt(s.d.Clock), From: t.Peer, FromSession: l.RemoteName, ToSession: t.FromSession,
			LinkID: t.LinkID, Kind: core.KindTaskUpdate, Body: body, TaskID: t.ID,
		})
		errs = append(errs, derr)
	}
	return errors.Join(append(errs, err)...)
}

// LinkLowered implements LinkLowerObserver: when a link drops to messages,
// its tasks still waiting (for approval or a claim) are rejected.
func (s *TaskService) LinkLowered(ctx context.Context, l store.Link) error {
	if s.d.Policy.Decide(l.PermissionIn, core.KindTaskCreate) != DecisionReject {
		return nil
	}
	ts, err := s.d.Tasks.ListTasks(ctx, store.TaskFilter{Direction: store.TaskInbound, States: pendingStates, LinkID: l.ID, Peer: l.Peer})
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
