package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/filecrypt"
	"github.com/cravv/cravv-connect/internal/pathguard"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

const (
	// MaxDownloadAttempts is how many times a download is tried before it fails.
	MaxDownloadAttempts = 3
	// DiskHeadroom is kept free on top of an incoming file's size.
	DiskHeadroom = 64 << 20
)

var (
	errDiskFull = errors.New("not enough disk space")
	// errKilled stops a download when the kill switch flips; it resumes on resume.
	errKilled = errors.New("kill switch on")
)

// OutboundChecker validates a path a session wants to send and opens it.
// The returned file is the very file the checks approved (no symlink swap,
// a single hard link); info comes from fstat and the caller reads at most
// info.Size() bytes and closes the file. Implemented by *AllowPaths.
type OutboundChecker interface {
	Open(ctx context.Context, projectDir, path string) (*os.File, os.FileInfo, error)
}

// FileDeps are the FileService collaborators.
type FileDeps struct {
	Blobs      func() transport.BlobStore // signed with the current identity
	Peers      store.PeerStore
	Files      store.FileStore
	Inbox      *InboxService
	Sender     EnvelopeSender
	Guard      OutboundChecker
	FilesDir   string
	Quota      int64 // per-peer inbound bytes; <= 0 means core.DefaultPeerQuota
	Policy     TrustPolicy
	Clock      core.Clock
	Audit      audit.Logger
	Log        *slog.Logger
	FreeSpace  func(dir string) (uint64, error) // nil means statfs
	RetryDelay time.Duration                    // pause between download attempts
	Killed     func() bool                      // kill switch state; nil means never killed
}

// FileService sends files through relay blobs and receives offered files (spec 5.3, 7.1, 7.4).
type FileService struct {
	d FileDeps

	mu       sync.Mutex
	baseCtx  context.Context
	inflight map[string]context.CancelFunc // running downloads by file ID
	wg       sync.WaitGroup
}

// NewFileService builds a FileService.
func NewFileService(d FileDeps) *FileService {
	if d.Quota <= 0 {
		d.Quota = core.DefaultPeerQuota
	}
	if d.Audit == nil {
		d.Audit = audit.Nop{}
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.FreeSpace == nil {
		d.FreeSpace = diskFree
	}
	if d.Killed == nil {
		d.Killed = func() bool { return false }
	}
	return &FileService{d: d, baseCtx: context.Background(), inflight: map[string]context.CancelFunc{}}
}

// Start sets the context downloads run under and resumes downloads that were
// in progress when the daemon stopped (none while the kill switch is on).
func (s *FileService) Start(ctx context.Context) error {
	s.mu.Lock()
	s.baseCtx = ctx
	s.mu.Unlock()
	return s.ResumeDownloads(ctx)
}

// ResumeDownloads starts every inbound download left in the downloading state
// (after a restart or when the kill switch is turned off). It does nothing
// while killed. Downloads run under the context given to Start.
func (s *FileService) ResumeDownloads(ctx context.Context) error {
	if s.d.Killed() {
		return nil
	}
	recs, err := s.d.Files.ListFiles(ctx, store.FileDownloading)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Direction == store.TaskInbound {
			s.startDownload(r.FileID)
		}
	}
	return nil
}

// StopDownloads cancels every running download (kill switch). Stopped
// downloads stay in the downloading state and continue from their last chunk
// on ResumeDownloads.
func (s *FileService) StopDownloads() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.inflight {
		cancel()
	}
}

// Wait blocks until every running download has finished.
func (s *FileService) Wait() { s.wg.Wait() }

// List returns every file record without its key, so no API can leak file keys.
func (s *FileService) List(ctx context.Context) ([]store.FileRecord, error) {
	recs, err := s.d.Files.ListFiles(ctx)
	for i := range recs {
		recs[i].Key = nil
	}
	return recs, err
}

// SendFile checks the path, encrypts and uploads it, and sends a file.offer.
func (s *FileService) SendFile(ctx context.Context, to core.MachineID, projectDir, path, taskID string) (core.FileRef, error) {
	f, info, err := s.d.Guard.Open(ctx, projectDir, path)
	if err != nil {
		return core.FileRef{}, err
	}
	defer f.Close()
	abs := f.Name()
	peer, err := s.d.Peers.GetPeer(ctx, to)
	if err != nil {
		return core.FileRef{}, err
	}
	if peer.Paused {
		return core.FileRef{}, core.ErrPaused
	}
	size := info.Size()
	if size > core.MaxFileBytes {
		return core.FileRef{}, fmt.Errorf("%s: %w", abs, core.ErrTooLarge)
	}
	key, err := filecrypt.NewKey()
	if err != nil {
		return core.FileRef{}, err
	}
	rec := store.FileRecord{
		FileID: core.NewID(), Direction: store.TaskOutbound, Peer: to, Name: filepath.Base(abs),
		Size: size, Chunks: filecrypt.ChunkCount(size), TaskID: taskID, State: store.FileUploading,
		LocalPath: abs, CreatedAt: s.d.Clock.Now(),
	}
	if err := s.d.Files.PutFile(ctx, rec); err != nil {
		return core.FileRef{}, err
	}
	blobID, sum, err := s.upload(ctx, peer, rec, key, f)
	if err != nil {
		s.markOutFailed(ctx, rec.FileID, err)
		return core.FileRef{}, err
	}
	_ = s.d.Audit.Record(audit.Event{
		Type: audit.EvFileOut, Peer: to, Alias: peer.Alias, ItemID: rec.FileID, Hash: hex.EncodeToString(sum),
		Detail: map[string]any{"path": abs, "size": size},
	})
	offer := core.FileOfferBody{
		FileID: rec.FileID, BlobID: blobID, Name: rec.Name, Size: size, Chunks: rec.Chunks,
		SHA256: sum, Key: key, TaskID: taskID,
	}
	msgID, err := s.d.Sender.SendEnvelope(ctx, to, core.KindFileOffer, "", "", offer)
	if err != nil {
		s.markOutFailed(ctx, rec.FileID, err)
		return core.FileRef{}, err
	}
	if _, err := s.d.Files.UpdateFile(ctx, rec.FileID, func(f *store.FileRecord) error {
		f.State, f.MsgID, f.BlobID, f.SHA256, f.NextChunk = store.FileSent, msgID, blobID, sum, rec.Chunks
		return nil
	}); err != nil {
		return core.FileRef{}, err
	}
	return core.FileRef{FileID: rec.FileID, Name: rec.Name, Size: size}, nil
}

// upload streams at most rec.Size bytes of f, which the guard opened and
// fstat'ed, so a file that grows is caught by a second fstat, not by reading on.
func (s *FileService) upload(ctx context.Context, peer store.Peer, rec store.FileRecord, key []byte, f *os.File) (string, []byte, error) {
	r := io.LimitReader(f, rec.Size)
	blobs := s.d.Blobs()
	blobID, err := blobs.Create(ctx, peer.IK, rec.Size, rec.Chunks)
	if err != nil {
		return "", nil, err
	}
	h := sha256.New()
	buf := make([]byte, core.FileChunkBytes)
	for i := uint32(0); i < rec.Chunks; i++ {
		want := chunkLen(rec.Size, i)
		if _, err := io.ReadFull(r, buf[:want]); err != nil {
			return "", nil, fmt.Errorf("file changed while sending: %w", err)
		}
		h.Write(buf[:want])
		ct, err := filecrypt.EncryptChunk(key, rec.FileID, i, i == rec.Chunks-1, buf[:want])
		if err != nil {
			return "", nil, err
		}
		if err := blobs.PutChunk(ctx, blobID, i, ct); err != nil {
			return "", nil, err
		}
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != rec.Size {
		return "", nil, errors.New("file changed while sending: its size changed")
	}
	return blobID, h.Sum(nil), nil
}

func (s *FileService) markOutFailed(ctx context.Context, id string, cause error) {
	_, _ = s.d.Files.UpdateFile(ctx, id, func(f *store.FileRecord) error {
		f.State, f.Reason = store.FileFailed, cause.Error()
		return nil
	})
}

// chunkLen is the plaintext length of chunk i of a file of the given size.
func chunkLen(size int64, i uint32) int64 {
	rest := size - int64(i)*core.FileChunkBytes
	if rest > core.FileChunkBytes {
		return core.FileChunkBytes
	}
	return rest
}

// HandleOffer records an incoming file.offer and holds, declines or downloads it.
// Behind a PolicyGate its own decision must match the gate's.
func (s *FileService) HandleOffer(ctx context.Context, peer store.Peer, env core.Envelope) error {
	return s.handleOffer(ctx, peer, env, s.d.Policy.Decide(peer.TrustIn, core.KindFileOffer))
}

// RejectOffer records an incoming file.offer as declined ("not permitted") and tells the
// sender. It is the PolicyGate's OnReject for file.offer.
func (s *FileService) RejectOffer(ctx context.Context, peer store.Peer, env core.Envelope) error {
	return s.handleOffer(ctx, peer, env, DecisionReject)
}

func (s *FileService) handleOffer(ctx context.Context, peer store.Peer, env core.Envelope, decision Decision) error {
	b, err := decodeEnvBody[core.FileOfferBody](env.Body)
	if err != nil {
		return err
	}
	if !core.ValidID(b.FileID) || !core.ValidBlobID(b.BlobID) || (b.TaskID != "" && !core.ValidID(b.TaskID)) {
		return fmt.Errorf("file.offer %s: %w", env.ID, errBadPeerID)
	}
	if !safeID(env.ID) || b.Size < 0 || b.Size > core.MaxFileBytes ||
		b.Chunks != filecrypt.ChunkCount(b.Size) || len(b.SHA256) != sha256.Size || len(b.Key) != 32 {
		return fmt.Errorf("file.offer %s: malformed", env.ID)
	}
	if err := checkGateDecision(ctx, decision, core.KindFileOffer, b.FileID); err != nil {
		return err
	}
	if cur, err := s.d.Files.GetFile(ctx, b.FileID); err == nil {
		return Retryable(s.redeliverOffer(ctx, peer, cur, env.ID))
	} else if !errors.Is(err, core.ErrNotFound) {
		return Retryable(err)
	}
	local, err := pathguard.InboundPath(s.d.FilesDir, peer.Alias, env.ID, b.Name)
	if err != nil {
		return err
	}
	rec := store.FileRecord{
		FileID: b.FileID, Direction: store.TaskInbound, Peer: peer.MachineID, MsgID: env.ID, BlobID: b.BlobID,
		Name: pathguard.SanitizeName(b.Name), Size: b.Size, Chunks: b.Chunks, SHA256: b.SHA256, Key: b.Key,
		TaskID: b.TaskID, LocalPath: local, CreatedAt: s.d.Clock.Now(),
	}
	switch decision {
	case DecisionHold:
		rec.State, rec.Reason = store.FileHeld, "held for a human to accept"
		if err := s.d.Files.PutFile(ctx, rec); err != nil {
			return Retryable(err)
		}
		return Retryable(s.notice(ctx, rec))
	case DecisionReject:
		rec.State, rec.Reason = store.FileDeclined, "not permitted"
		if err := s.d.Files.PutFile(ctx, rec); err != nil {
			return Retryable(err)
		}
		return Retryable(s.declined(ctx, peer, rec))
	}
	if err := s.admit(ctx, rec); err != nil {
		rec.State, rec.Reason = store.FileDeclined, err.Error()
		if perr := s.d.Files.PutFile(ctx, rec); perr != nil {
			return Retryable(perr)
		}
		return Retryable(s.declined(ctx, peer, rec))
	}
	rec.State = store.FileDownloading
	if err := s.d.Files.PutFile(ctx, rec); err != nil {
		return Retryable(err)
	}
	s.startDownload(rec.FileID)
	return nil
}

// redeliverOffer handles a file.offer whose record already exists. When it is a
// redelivery of the offer that created a held or declined record and the
// earlier attempt failed before the inbox notice was written, the notice (and
// for a decline, the note to the sender) is written now. Anything else is a
// duplicate and ignored.
func (s *FileService) redeliverOffer(ctx context.Context, peer store.Peer, cur store.FileRecord, msgID string) error {
	if cur.Direction != store.TaskInbound || cur.Peer != peer.MachineID || cur.MsgID != msgID {
		return nil
	}
	if cur.State != store.FileHeld && cur.State != store.FileDeclined {
		return nil
	}
	done, err := s.d.Inbox.Delivered(ctx, msgID)
	if err != nil || done {
		return err
	}
	if cur.State == store.FileHeld {
		return s.notice(ctx, cur)
	}
	return s.declined(ctx, peer, cur)
}

// Accept downloads a held file. It is a human-only action: unlocked must be
// true (set by the IPC layer after auth.unlock), otherwise core.ErrAuthRequired.
func (s *FileService) Accept(ctx context.Context, fileID string, unlocked bool) error {
	if !unlocked {
		return core.ErrAuthRequired
	}
	rec, err := s.d.Files.GetFile(ctx, fileID)
	if err != nil {
		return err
	}
	if rec.Direction != store.TaskInbound || rec.State != store.FileHeld {
		return fmt.Errorf("file %s is %s, not held: %w", fileID, rec.State, core.ErrBadTransition)
	}
	peer, err := s.d.Peers.GetPeer(ctx, rec.Peer)
	if err != nil {
		return fmt.Errorf("peer %s: %w", rec.Peer.Short(), err)
	}
	if peer.Paused {
		return fmt.Errorf("%s: %w", peer.Alias, core.ErrPaused)
	}
	if s.d.Policy.Decide(peer.TrustIn, core.KindFileOffer) == DecisionReject {
		return fmt.Errorf("%s is %s: %w", peer.Alias, peer.TrustIn, core.ErrNotPermitted)
	}
	// The state is checked again inside each update, so of two concurrent
	// accepts exactly one moves the file on.
	fromHeld := func(state store.FileState, reason string) func(f *store.FileRecord) error {
		return func(f *store.FileRecord) error {
			if f.State != store.FileHeld {
				return fmt.Errorf("file %s is %s, not held: %w", fileID, f.State, core.ErrBadTransition)
			}
			f.State, f.Reason = state, reason
			return nil
		}
	}
	if aerr := s.admit(ctx, rec); aerr != nil {
		rec, err = s.d.Files.UpdateFile(ctx, fileID, fromHeld(store.FileDeclined, aerr.Error()))
		if err != nil {
			return err
		}
		if err := s.declined(ctx, peer, rec); err != nil {
			return err
		}
		return aerr
	}
	if _, err := s.d.Files.UpdateFile(ctx, fileID, fromHeld(store.FileDownloading, "")); err != nil {
		return err
	}
	_ = s.d.Audit.Record(audit.Event{Type: audit.EvFileAccept, Peer: rec.Peer, Alias: peer.Alias, ItemID: fileID, Hash: hex.EncodeToString(rec.SHA256)})
	s.startDownload(fileID)
	return nil
}

// PeerCutOff implements PeerCutOffObserver: the peer's held files are declined
// and the sender is told (best effort).
func (s *FileService) PeerCutOff(ctx context.Context, peer store.Peer, reason string) error {
	recs, err := s.d.Files.ListFiles(ctx, store.FileHeld)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Direction != store.TaskInbound || r.Peer != peer.MachineID {
			continue
		}
		rec, err := s.d.Files.UpdateFile(ctx, r.FileID, func(f *store.FileRecord) error {
			if f.State != store.FileHeld {
				return core.ErrBadTransition
			}
			f.State, f.Reason = store.FileDeclined, reason
			return nil
		})
		if errors.Is(err, core.ErrBadTransition) {
			continue
		}
		if err != nil {
			return err
		}
		_ = s.declined(ctx, peer, rec)
	}
	return nil
}

// admit checks the per-peer quota and free disk space for an incoming file.
// The quota counts running downloads and files downloaded within
// core.InboxRetention (older records are purged by maintenance).
func (s *FileService) admit(ctx context.Context, rec store.FileRecord) error {
	used, err := s.d.Files.InboundBytes(ctx, rec.Peer, s.d.Clock.Now().Add(-core.InboxRetention))
	if err != nil {
		return err
	}
	if used+rec.Size > s.d.Quota {
		return core.ErrQuota
	}
	if err := os.MkdirAll(s.d.FilesDir, 0o700); err != nil {
		return err
	}
	if free, err := s.d.FreeSpace(s.d.FilesDir); err == nil && free < uint64(rec.Size)+DiskHeadroom {
		return errDiskFull
	}
	return nil
}

// startDownload runs one download in the background. The kill check and the
// registration happen under s.mu, so StopDownloads cancels every download that
// started before the switch flipped and none starts after.
func (s *FileService) startDownload(fileID string) {
	s.mu.Lock()
	if _, running := s.inflight[fileID]; running || s.d.Killed() {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.baseCtx)
	s.inflight[fileID] = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, fileID)
			s.mu.Unlock()
			cancel()
		}()
		s.download(ctx, fileID)
	}()
}

func (s *FileService) download(ctx context.Context, fileID string) {
	for {
		rec, err := s.d.Files.GetFile(ctx, fileID)
		if err != nil || rec.State != store.FileDownloading {
			return
		}
		ferr := s.fetch(ctx, rec)
		if s.d.Killed() || errors.Is(ferr, errKilled) {
			return // stopped by the kill switch: ResumeDownloads continues it
		}
		if ferr == nil {
			s.finish(ctx, rec)
			return
		}
		if ctx.Err() != nil {
			return // daemon stopping: Start resumes it next time
		}
		s.d.Log.Warn("file download attempt failed", "file_id", fileID, "err", ferr)
		rec, err = s.d.Files.UpdateFile(ctx, fileID, func(f *store.FileRecord) error {
			f.Attempts++
			f.Reason = ferr.Error()
			return nil
		})
		if err != nil {
			return
		}
		if rec.Attempts >= MaxDownloadAttempts {
			s.fail(ctx, rec, ferr)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.d.RetryDelay):
		}
	}
}

// fetch downloads the remaining chunks into <LocalPath>.part, verifies the
// SHA-256, and links the result to LocalPath (never overwriting a file).
func (s *FileService) fetch(ctx context.Context, rec store.FileRecord) error {
	if rec.NextChunk >= rec.Chunks && verifyFile(rec.LocalPath, rec.Size, rec.SHA256) == nil {
		return nil // finished before a stop; only the bookkeeping is left
	}
	if err := os.MkdirAll(filepath.Dir(rec.LocalPath), 0o700); err != nil {
		return err
	}
	part := rec.LocalPath + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	off := int64(rec.NextChunk) * core.FileChunkBytes
	if err := f.Truncate(off); err != nil {
		return err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return err
	}
	blobs := s.d.Blobs()
	for i := rec.NextChunk; i < rec.Chunks; i++ {
		if s.d.Killed() {
			return errKilled
		}
		ct, err := blobs.GetChunk(ctx, rec.BlobID, i)
		if err != nil {
			return fmt.Errorf("chunk %d: %w", i, err)
		}
		pt, err := filecrypt.DecryptChunk(rec.Key, rec.FileID, i, i == rec.Chunks-1, ct)
		if err != nil {
			return fmt.Errorf("chunk %d: %w", i, err)
		}
		if int64(len(pt)) != chunkLen(rec.Size, i) {
			return fmt.Errorf("chunk %d: wrong length %d", i, len(pt))
		}
		if _, err := f.Write(pt); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		next := i + 1
		if _, err := s.d.Files.UpdateFile(ctx, rec.FileID, func(r *store.FileRecord) error {
			r.NextChunk = next
			return nil
		}); err != nil {
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if s.d.Killed() {
		return errKilled
	}
	if err := verifyFile(part, rec.Size, rec.SHA256); err != nil {
		os.Remove(part)
		_, _ = s.d.Files.UpdateFile(ctx, rec.FileID, func(r *store.FileRecord) error {
			r.NextChunk = 0
			return nil
		})
		return err
	}
	if err := os.Link(part, rec.LocalPath); err != nil {
		if !errors.Is(err, fs.ErrExist) || verifyFile(rec.LocalPath, rec.Size, rec.SHA256) != nil {
			return err
		}
	}
	return os.Remove(part)
}

// safeID accepts the IDs core.NewID produces (and similar): 1 to 64 ASCII
// letters or digits. Peer-chosen IDs become part of local file names.
func safeID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
			return false
		}
	}
	return true
}

func verifyFile(path string, size int64, want []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != size || !bytes.Equal(h.Sum(nil), want) {
		return errors.New("sha256 mismatch")
	}
	return nil
}

func (s *FileService) finish(ctx context.Context, rec store.FileRecord) {
	if err := s.d.Blobs().Delete(ctx, rec.BlobID); err != nil {
		s.d.Log.Warn("blob delete failed", "file_id", rec.FileID, "err", err)
	}
	done, err := s.d.Files.UpdateFile(ctx, rec.FileID, func(f *store.FileRecord) error {
		f.State, f.Reason = store.FileDone, ""
		return nil
	})
	if err != nil {
		s.d.Log.Error("file record update failed", "file_id", rec.FileID, "err", err)
		return
	}
	rec = done
	alias := s.aliasOf(ctx, rec.Peer)
	_ = s.d.Audit.Record(audit.Event{
		Type: audit.EvFileIn, Peer: rec.Peer, Alias: alias, ItemID: rec.FileID, Hash: hex.EncodeToString(rec.SHA256),
		Detail: map[string]any{"path": rec.LocalPath, "size": rec.Size},
	})
	if err := s.notice(ctx, rec); err != nil {
		s.d.Log.Error("file notice failed", "file_id", rec.FileID, "err", err)
	}
}

func (s *FileService) fail(ctx context.Context, rec store.FileRecord, cause error) {
	os.Remove(rec.LocalPath + ".part")
	rec, err := s.d.Files.UpdateFile(ctx, rec.FileID, func(f *store.FileRecord) error {
		f.State, f.Reason = store.FileFailed, "download failed: "+cause.Error()
		return nil
	})
	if err != nil {
		return
	}
	if peer, err := s.d.Peers.GetPeer(ctx, rec.Peer); err == nil {
		_ = s.declined(ctx, peer, rec)
	}
}

// declined puts a local notice in the inbox and tells the sender why.
func (s *FileService) declined(ctx context.Context, peer store.Peer, rec store.FileRecord) error {
	if err := s.notice(ctx, rec); err != nil {
		return err
	}
	text := fmt.Sprintf("cravv-connect: file %q (file_id %s) was not received: %s", rec.Name, rec.FileID, rec.Reason)
	_, err := s.d.Sender.SendEnvelope(ctx, peer.MachineID, core.KindChat, "", "", core.ChatBody{Text: text})
	return err
}

func (s *FileService) notice(ctx context.Context, rec store.FileRecord) error {
	n := FileNotice{FileID: rec.FileID, Name: rec.Name, Size: rec.Size, State: rec.State, Reason: rec.Reason}
	if rec.State == store.FileDone {
		n.Path = rec.LocalPath
	}
	body, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = s.d.Inbox.Deliver(ctx, store.InboxItem{
		MsgID: rec.MsgID, From: rec.Peer, Kind: core.KindFileOffer, Body: body, TaskID: rec.TaskID,
	})
	return err
}

func (s *FileService) aliasOf(ctx context.Context, id core.MachineID) string {
	if p, err := s.d.Peers.GetPeer(ctx, id); err == nil {
		return p.Alias
	}
	return id.Short()
}
