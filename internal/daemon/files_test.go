package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/audit"
	"github.com/cravv/cravv-connect/internal/core"
	"github.com/cravv/cravv-connect/internal/filecrypt"
	"github.com/cravv/cravv-connect/internal/store"
	"github.com/cravv/cravv-connect/internal/transport"
)

// d2Blobs is an in-memory transport.BlobStore with fault injection.
type d2Blobs struct {
	mu       sync.Mutex
	blobs    map[string]map[uint32][]byte
	deleted  map[string]bool
	getCalls map[uint32]int
	failGet  map[uint32]int                            // chunk -> remaining transient failures
	onGet    func(n uint32)                            // called before GetChunk serves chunk n (outside the lock)
	onPut    func(ctx context.Context, n uint32) error // called before PutChunk stores chunk n
	puts     map[uint32]int
}

func newD2Blobs() *d2Blobs {
	return &d2Blobs{blobs: map[string]map[uint32][]byte{}, deleted: map[string]bool{}, getCalls: map[uint32]int{}, failGet: map[uint32]int{}}
}

func (b *d2Blobs) Create(ctx context.Context, recipient ed25519.PublicKey, size int64, chunks uint32) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := strings.ToLower(core.NewID()) // relays issue [a-z0-9] blob IDs
	b.blobs[id] = map[uint32][]byte{}
	return id, nil
}

func (b *d2Blobs) PutChunk(ctx context.Context, blobID string, n uint32, data []byte) error {
	b.mu.Lock()
	hook := b.onPut
	b.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, n); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.puts == nil {
		b.puts = map[uint32]int{}
	}
	b.puts[n]++
	b.blobs[blobID][n] = append([]byte(nil), data...)
	return nil
}

func (b *d2Blobs) GetChunk(ctx context.Context, blobID string, n uint32) ([]byte, error) {
	b.mu.Lock()
	hook := b.onGet
	b.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.getCalls[n]++
	if b.failGet[n] > 0 {
		b.failGet[n]--
		return nil, errors.New("connection reset")
	}
	c, ok := b.blobs[blobID][n]
	if !ok {
		return nil, core.ErrNotFound
	}
	return append([]byte(nil), c...), nil
}

func (b *d2Blobs) Delete(ctx context.Context, blobID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deleted[blobID] = true
	return nil
}

// put encrypts content as a blob and returns a matching file.offer body.
func (b *d2Blobs) put(t *testing.T, name string, content []byte) core.FileOfferBody {
	t.Helper()
	key, _ := filecrypt.NewKey()
	fileID := core.NewID()
	chunks := filecrypt.ChunkCount(int64(len(content)))
	blobID, _ := b.Create(context.Background(), nil, int64(len(content)), chunks)
	for i := uint32(0); i < chunks; i++ {
		lo := int64(i) * core.FileChunkBytes
		hi := min(lo+core.FileChunkBytes, int64(len(content)))
		ct, err := filecrypt.EncryptChunk(key, fileID, i, i == chunks-1, content[lo:hi])
		if err != nil {
			t.Fatal(err)
		}
		b.PutChunk(context.Background(), blobID, i, ct)
	}
	sum := sha256.Sum256(content)
	return core.FileOfferBody{FileID: fileID, BlobID: blobID, Name: name, Size: int64(len(content)), Chunks: chunks, SHA256: sum[:], Key: key}
}

type d2FileEnv struct {
	killed   atomic.Bool
	te       *d2TaskEnv
	blobs    *d2Blobs
	filesDir string
	project  string
	free     uint64
	files    *FileService
}

// d2FileSvc is a FileService on the d2TaskEnv machine: gpu-box may send
// messages (and so files) on the link to session "lead".
func d2FileSvc(t *testing.T, quota int64) *d2FileEnv {
	t.Helper()
	e := &d2FileEnv{te: d2Tasks(t, core.PermMessages), blobs: newD2Blobs(), filesDir: filepath.Join(t.TempDir(), "files"), project: t.TempDir(), free: 1 << 40}
	e.files = NewFileService(FileDeps{
		Blobs: func() transport.BlobStore { return e.blobs }, Peers: e.te.st, Links: e.te.st, Files: e.te.st, Inbox: e.te.inbox,
		Sender: e.te.sender, Guard: NewAllowPaths(e.te.st, e.te.audit), FilesDir: e.filesDir, Quota: quota,
		Clock: e.te.clock, Audit: e.te.audit,
		FreeSpace: func(string) (uint64, error) { return e.free, nil },
		Killed:    e.killed.Load,
	})
	e.te.links.AddCloseObserver(e.files)
	return e
}

// offerOn runs a file.offer from peer on link l through the LinkGate.
func (e *d2FileEnv) offerOn(t *testing.T, peer store.Peer, l store.Link, body core.FileOfferBody) core.Envelope {
	t.Helper()
	env := d2Env(t, peer, core.KindFileOffer, l.ID, body)
	g := d2Gated(e.te.st, e.te.shared, e.te.replies, HandlerFunc(e.files.HandleOffer), HandlerFunc(e.files.RejectOffer))
	if err := g.Handle(context.Background(), peer, env); err != nil {
		t.Fatalf("file.offer: %v", err)
	}
	e.files.Wait()
	return env
}

// offer runs a file.offer from gpu-box on the environment's link.
func (e *d2FileEnv) offer(t *testing.T, body core.FileOfferBody) core.Envelope {
	t.Helper()
	return e.offerOn(t, e.te.peer, e.te.link, body)
}

// hold stores fileID as an inbound file held on the environment's link, the
// way an older version held files for a human to accept.
func (e *d2FileEnv) hold(t *testing.T, fileID string, body core.FileOfferBody) {
	t.Helper()
	l := e.te.link
	if err := e.te.st.PutFile(context.Background(), store.FileRecord{
		FileID: fileID, Direction: store.TaskInbound, Peer: e.te.peer.MachineID, MsgID: core.NewID(), BlobID: body.BlobID,
		Name: body.Name, Size: body.Size, Chunks: body.Chunks, SHA256: body.SHA256, Key: body.Key,
		LinkID: l.ID, Session: l.Session, State: store.FileHeld,
		LocalPath: filepath.Join(e.filesDir, "gpu-box", fileID+"-"+body.Name), CreatedAt: d2Epoch,
	}); err != nil {
		t.Fatal(err)
	}
}

func (e *d2FileEnv) record(t *testing.T, id string) store.FileRecord {
	t.Helper()
	r, err := e.te.st.GetFile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestSendFileUploadsAndOffers(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	peer := e.te.peer
	content := randomBytes(t, 2*core.FileChunkBytes+123)
	if err := os.WriteFile(filepath.Join(e.project, "data.bin"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := e.files.SendFile(ctx, e.te.link, e.project, "data.bin", "T1")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Name != "data.bin" || ref.Size != int64(len(content)) {
		t.Fatalf("ref = %+v", ref)
	}
	sent := e.te.sender.ofKind(core.KindFileOffer)
	if len(sent) != 1 || sent[0].To != peer.MachineID || sent[0].LinkID != e.te.link.ID {
		t.Fatalf("offers = %+v", sent)
	}
	var offer core.FileOfferBody
	json.Unmarshal(sent[0].Body, &offer)
	sum := sha256.Sum256(content)
	if offer.FileID != ref.FileID || offer.Chunks != 3 || !bytes.Equal(offer.SHA256, sum[:]) || offer.TaskID != "T1" || len(offer.Key) != 32 {
		t.Fatalf("offer = %+v", offer)
	}
	var got []byte
	for i := uint32(0); i < offer.Chunks; i++ {
		ct := e.blobs.blobs[offer.BlobID][i]
		pt, err := filecrypt.DecryptChunk(offer.Key, offer.FileID, i, i == offer.Chunks-1, ct)
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		got = append(got, pt...)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("uploaded content differs")
	}
	ev := e.te.audit.ofType(audit.EvFileOut)
	if len(ev) != 1 || ev[0].Hash != fmt.Sprintf("%x", sum) || ev[0].Alias != "gpu-box" {
		t.Fatalf("audit = %+v", ev)
	}
	if r := e.record(t, ref.FileID); r.State != store.FileSent || r.Direction != store.TaskOutbound || r.MsgID != sent[0].ID ||
		r.LinkID != e.te.link.ID || r.Session != e.te.session.ID {
		t.Fatalf("record = %+v", r)
	}
}

func TestSendFileRefusesSecrets(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	link := e.te.link
	os.WriteFile(filepath.Join(e.project, ".env"), []byte("TOKEN=x"), 0o600)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(outside, []byte("x"), 0o600)
	for _, p := range []string{".env", outside} {
		if _, err := e.files.SendFile(ctx, link, e.project, p, ""); !errors.Is(err, core.ErrPathRefused) {
			t.Errorf("SendFile(%s) err = %v, want ErrPathRefused", p, err)
		}
	}
	if err := e.files.d.Guard.(*AllowPaths).Add(ctx, filepath.Dir(outside), true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.files.SendFile(ctx, link, e.project, outside, ""); err != nil {
		t.Fatalf("allowed path refused: %v", err)
	}
	if len(e.te.audit.ofType(audit.EvAllowPath)) != 1 {
		t.Fatal("allow-path not audited")
	}

	os.WriteFile(filepath.Join(e.project, "ok.txt"), []byte("ok"), 0o600)
	paused, _ := d2Peer(t, e.te.st, "paused")
	paused.Paused = true
	e.te.st.PutPeer(ctx, paused)
	pausedLink := d2Link(t, e.te.st, paused, e.te.session, "x", core.PermMessages, core.PermMessages)
	if _, err := e.files.SendFile(ctx, pausedLink, e.project, "ok.txt", ""); !errors.Is(err, core.ErrPaused) {
		t.Fatalf("send to a peer we paused err = %v", err)
	}
	closed := link
	closed.State = store.LinkClosed
	if _, err := e.files.SendFile(ctx, closed, e.project, "ok.txt", ""); !errors.Is(err, core.ErrLinkClosed) {
		t.Fatalf("send on a closed link err = %v", err)
	}
}

// A hard link inside the project to a file stored elsewhere passes the path
// checks but must not be sent: the guard opens the file and refuses Nlink > 1.
func TestSendFileRefusesHardlinkedSecret(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("PRIVATE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(secret, filepath.Join(e.project, "notes.txt")); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	if _, err := e.files.SendFile(ctx, e.te.link, e.project, "notes.txt", ""); !errors.Is(err, core.ErrPathRefused) {
		t.Fatalf("SendFile(hard link) err = %v, want ErrPathRefused", err)
	}
	if n := len(e.te.sender.ofKind(core.KindFileOffer)); n != 0 {
		t.Fatalf("%d offers sent for a refused file", n)
	}
	if recs, _ := e.files.List(ctx); len(recs) != 0 {
		t.Fatalf("records for a refused file: %+v", recs)
	}
	if len(e.blobs.blobs) != 0 {
		t.Fatal("refused file was uploaded")
	}
}

func TestReceiveFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	session := e.te.session.ID
	content := randomBytes(t, core.FileChunkBytes+5)
	body := e.blobs.put(t, "model.pt", content)
	env := e.offer(t, body)

	r := e.record(t, body.FileID)
	if r.State != store.FileDone || r.LinkID != e.te.link.ID || r.Session != session {
		t.Fatalf("record = %+v (%s)", r, r.Reason)
	}
	want := filepath.Join(e.filesDir, "gpu-box", env.ID+"-model.pt")
	if r.LocalPath != want {
		t.Fatalf("path = %s, want %s", r.LocalPath, want)
	}
	got, err := os.ReadFile(want)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: %v", err)
	}
	if _, err := os.Stat(want + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part file left behind")
	}
	if !e.blobs.deleted[body.BlobID] {
		t.Fatal("blob not deleted after download")
	}
	items, _ := e.te.inbox.Check(ctx, session, 10)
	if len(items) != 1 || items[0].Kind != "file" || items[0].Path != want || items[0].FileID != body.FileID {
		t.Fatalf("inbox = %+v", items)
	}
	if ev := e.te.audit.ofType(audit.EvFileIn); len(ev) != 1 || ev[0].ItemID != body.FileID {
		t.Fatalf("audit = %+v", ev)
	}
	// A duplicate offer changes nothing.
	if err := e.files.HandleOffer(withLink(ctx, e.te.link), e.te.peer, env); err != nil {
		t.Fatal(err)
	}
	e.files.Wait()
	if len(e.te.audit.ofType(audit.EvFileIn)) != 1 {
		t.Fatal("duplicate offer downloaded again")
	}
}

func TestReceivePathStaysInside(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	peer := e.te.peer
	names := []string{"../../.ssh/authorized_keys", ".bashrc", strings.Repeat("n", 300), "notes.txt", "notes.txt", "", `..\..\evil.bat`}
	aliasDir := filepath.Join(e.filesDir, "gpu-box") + string(filepath.Separator)
	seen := map[string]bool{}
	for i, name := range names {
		content := []byte(fmt.Sprintf("payload %d", i))
		body := e.blobs.put(t, name, content)
		e.offer(t, body)
		r := e.record(t, body.FileID)
		if r.State != store.FileDone {
			t.Fatalf("%q: state %s (%s)", name, r.State, r.Reason)
		}
		if !strings.HasPrefix(r.LocalPath, aliasDir) {
			t.Fatalf("%q landed outside files/gpu-box: %s", name, r.LocalPath)
		}
		base := filepath.Base(r.LocalPath)
		if strings.HasPrefix(base, ".") || strings.ContainsAny(base, `/\`) || len(base) > 130 {
			t.Fatalf("%q: bad saved name %q", name, base)
		}
		if seen[r.LocalPath] {
			t.Fatalf("%q overwrote %s", name, r.LocalPath)
		}
		seen[r.LocalPath] = true
		if got, _ := os.ReadFile(r.LocalPath); !bytes.Equal(got, content) {
			t.Fatalf("%q: content %q", name, got)
		}
	}
	entries, _ := os.ReadDir(aliasDir)
	if len(entries) != len(names) {
		t.Fatalf("files dir has %d entries, want %d", len(entries), len(names))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.filesDir), ".ssh")); !os.IsNotExist(err) {
		t.Fatal("a .ssh directory was created")
	}

	hostile := e.blobs.put(t, "x", []byte("x"))
	env := d2Env(t, peer, core.KindFileOffer, e.te.link.ID, hostile)
	env.ID = "../../escape"
	if err := e.files.HandleOffer(withLink(ctx, e.te.link), peer, env); err == nil {
		t.Fatal("offer with a path-like message id accepted")
	}
}

func TestReceiveResumesAfterInterruption(t *testing.T) {
	e := d2FileSvc(t, 0)
	content := randomBytes(t, 3*core.FileChunkBytes)
	body := e.blobs.put(t, "big.bin", content)
	e.blobs.failGet[1] = 1
	e.offer(t, body)
	r := e.record(t, body.FileID)
	if r.State != store.FileDone || r.Attempts != 1 {
		t.Fatalf("state %s attempts %d (%s)", r.State, r.Attempts, r.Reason)
	}
	if e.blobs.getCalls[0] != 1 || e.blobs.getCalls[1] != 2 || e.blobs.getCalls[2] != 1 {
		t.Fatalf("chunk fetches = %v, want chunk 0 fetched once (resume)", e.blobs.getCalls)
	}
	if got, _ := os.ReadFile(r.LocalPath); !bytes.Equal(got, content) {
		t.Fatal("resumed file differs")
	}
}

func TestReceiveTamperedChunkFails(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	peer, session := e.te.peer, e.te.session.ID
	body := e.blobs.put(t, "x.bin", randomBytes(t, 2*core.FileChunkBytes))
	e.blobs.blobs[body.BlobID][1][10] ^= 0xff
	e.offer(t, body)
	r := e.record(t, body.FileID)
	if r.State != store.FileFailed || r.Attempts != MaxDownloadAttempts {
		t.Fatalf("state %s attempts %d", r.State, r.Attempts)
	}
	if _, err := os.Stat(r.LocalPath); !os.IsNotExist(err) {
		t.Fatal("tampered file saved")
	}
	if _, err := os.Stat(r.LocalPath + ".part"); !os.IsNotExist(err) {
		t.Fatal(".part left behind")
	}
	items, _ := e.te.inbox.Check(ctx, session, 10)
	if len(items) != 1 || !strings.Contains(items[0].Wrapped, "failed") {
		t.Fatalf("inbox = %+v", items)
	}
	chats := e.te.sender.ofKind(core.KindChat)
	if len(chats) != 1 || chats[0].To != peer.MachineID || chats[0].LinkID != e.te.link.ID || !strings.Contains(string(chats[0].Body), "was not received") {
		t.Fatalf("sender not told: %+v", chats)
	}
}

func TestReceiveQuotaAndDisk(t *testing.T) {
	e := d2FileSvc(t, 2*core.FileChunkBytes)
	first := e.blobs.put(t, "a.bin", randomBytes(t, core.FileChunkBytes+1))
	e.offer(t, first)
	if e.record(t, first.FileID).State != store.FileDone {
		t.Fatal("first file within quota not downloaded")
	}
	second := e.blobs.put(t, "b.bin", randomBytes(t, core.FileChunkBytes))
	e.offer(t, second)
	r := e.record(t, second.FileID)
	if r.State != store.FileDeclined || r.Reason != core.ErrQuota.Error() {
		t.Fatalf("over-quota file: %s (%s)", r.State, r.Reason)
	}
	if e.blobs.getCalls[0] != 1 {
		t.Fatal("declined file was downloaded")
	}

	e.free = 1024
	other, _ := d2Peer(t, e.te.st, "mac")
	otherLink := d2Link(t, e.te.st, other, e.te.session, "laptop", core.PermMessages, core.PermMessages)
	third := e.blobs.put(t, "c.bin", []byte("small"))
	e.offerOn(t, other, otherLink, third)
	if r := e.record(t, third.FileID); r.State != store.FileDeclined || r.Reason != "not enough disk space" {
		t.Fatalf("disk-full file: %s (%s)", r.State, r.Reason)
	}
	if n := len(e.te.sender.ofKind(core.KindChat)); n != 2 {
		t.Fatalf("senders told %d times, want 2", n)
	}
}

// No link level holds files any more (messages includes files), and there
// is no accept. A held file left over (the upgrade declines them) is
// declined when its link closes.
func TestHeldFileDeclinedWhenLinkCloses(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	body := e.blobs.put(t, "readme.md", []byte("hello"))
	e.hold(t, body.FileID, body)
	if err := e.te.links.Disconnect(ctx, "", e.te.link.Num); err != nil {
		t.Fatal(err)
	}
	if r := e.record(t, body.FileID); r.State != store.FileDeclined || r.Reason != ReasonLinkClosed {
		t.Fatalf("held file after the link closed: %s (%s)", r.State, r.Reason)
	}
}

func TestRejectsMalformedOffer(t *testing.T) {
	e := d2FileSvc(t, 0)
	peer := e.te.peer
	good := e.blobs.put(t, "a", []byte("abc"))
	cases := map[string]func(b *core.FileOfferBody){
		"chunk count": func(b *core.FileOfferBody) { b.Chunks = 5 },
		"too big":     func(b *core.FileOfferBody) { b.Size = core.MaxFileBytes + 1; b.Chunks = filecrypt.ChunkCount(b.Size) },
		"short hash":  func(b *core.FileOfferBody) { b.SHA256 = b.SHA256[:5] },
		"bad file id": func(b *core.FileOfferBody) { b.FileID = "../x" },
	}
	for name, mut := range cases {
		b := good
		mut(&b)
		env := d2Env(t, peer, core.KindFileOffer, e.te.link.ID, b)
		if err := e.files.HandleOffer(withLink(context.Background(), e.te.link), peer, env); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// blobRelay is a d2Relay whose blob store is an in-memory d2Blobs.
type blobRelay struct {
	d2Relay
	blobs *d2Blobs
}

func (r *blobRelay) Dialer() transport.Dialer                   { return r }
func (r *blobRelay) Blobs(transport.Signer) transport.BlobStore { return r.blobs }

// The kill switch stops a running download: nothing more is fetched, no inbox
// notice appears, and Start does not resume it while killed. Resume finishes it.
func TestKillStopsDownloadsOnClosedLinks(t *testing.T) {
	ctx := context.Background()
	blobs := newD2Blobs()
	d := d2NewDaemon(t, t.TempDir(), &blobRelay{blobs: blobs})
	defer d.Close()
	peer := newTestPeer(t, "gpu-box")
	mustPut(t, d.store, peer.rec)
	session := d2Share(t, d.Shared(), "lead")
	link := d2Link(t, d.store, peer.rec, session, "trainer", core.PermMessages, core.PermMessages)
	content := randomBytes(t, 4*core.FileChunkBytes)
	body := blobs.put(t, "big.bin", content)

	reached := make(chan struct{})
	release := make(chan struct{})
	blobs.onGet = func(n uint32) {
		if n == 1 {
			close(reached)
			<-release
		}
	}
	env := d2Env(t, peer.rec, core.KindFileOffer, link.ID, body)
	if err := d.Files().HandleOffer(withLink(ctx, link), peer.rec, env); err != nil {
		t.Fatal(err)
	}
	<-reached
	blobs.mu.Lock()
	blobs.onGet = nil
	blobs.mu.Unlock()
	killed := make(chan error, 1)
	go func() { killed <- d.Kill().Kill(ctx) }()
	d2Eventually(t, "switch on", d.Kill().Killed)
	close(release)
	if err := <-killed; err != nil {
		t.Fatal(err)
	}
	d.Files().Wait()

	// The kill switch closes every link, so the download stops for good:
	// it fails with link_closed and resume does not bring it back.
	r, err := d.store.GetFile(ctx, body.FileID)
	if err != nil || r.State != store.FileFailed || r.Reason != ReasonLinkClosed || r.Attempts != 0 {
		t.Fatalf("after kill: %+v %v", r, err)
	}
	if n := blobs.getCalls[2] + blobs.getCalls[3]; n != 0 {
		t.Fatalf("chunks fetched after kill: %v", blobs.getCalls)
	}
	if err := d.Kill().Resume(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := d.Files().ResumeDownloads(ctx); err != nil {
		t.Fatal(err)
	}
	d.Files().Wait()
	if n := blobs.getCalls[2] + blobs.getCalls[3]; n != 0 {
		t.Fatalf("a download on a closed link resumed: %v", blobs.getCalls)
	}
	for _, p := range []string{r.LocalPath, r.LocalPath + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind (%v)", p, err)
		}
	}
	items, _ := d.Inbox().Check(ctx, session.ID, 10)
	for _, it := range items {
		if it.FileID != "" {
			t.Fatalf("file notice for a download on a closed link: %+v", it)
		}
	}
}

// Downloaded files stop counting toward the quota after InboxRetention (the
// records are purged then; the files on disk are the human's).
func TestQuotaForgetsOldDownloads(t *testing.T) {
	e := d2FileSvc(t, 2*core.FileChunkBytes)
	first := e.blobs.put(t, "a.bin", randomBytes(t, core.FileChunkBytes+1))
	e.offer(t, first)
	second := e.blobs.put(t, "b.bin", randomBytes(t, core.FileChunkBytes))
	e.offer(t, second)
	if r := e.record(t, second.FileID); r.State != store.FileDeclined {
		t.Fatalf("over quota: %s", r.State)
	}
	e.te.clock.Advance(core.InboxRetention + time.Minute)
	third := e.blobs.put(t, "c.bin", randomBytes(t, core.FileChunkBytes))
	e.offer(t, third)
	if r := e.record(t, third.FileID); r.State != store.FileDone {
		t.Fatalf("after retention: %s (%s)", r.State, r.Reason)
	}
}

// A kill during an upload stops it: the chunk in flight is cancelled, no
// further chunk is sent, the partial blob is deleted and no file.offer goes out.
func TestKillStopsUpload(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	if err := os.WriteFile(filepath.Join(e.project, "big.bin"), randomBytes(t, 4*core.FileChunkBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	e.blobs.onPut = func(ctx context.Context, n uint32) error {
		if n != 1 {
			return nil
		}
		e.killed.Store(true)
		go e.files.StopTransfers()
		select {
		case <-ctx.Done(): // the upload's context follows the kill switch
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return nil
		}
	}
	_, err := e.files.SendFile(ctx, e.te.link, e.project, "big.bin", "")
	if !errors.Is(err, core.ErrKilled) {
		t.Fatalf("SendFile during kill err = %v", err)
	}
	if offers := e.te.sender.ofKind(core.KindFileOffer); len(offers) != 0 {
		t.Fatalf("file.offer sent after kill: %+v", offers)
	}
	e.blobs.mu.Lock()
	puts, deleted := e.blobs.puts[2]+e.blobs.puts[3], len(e.blobs.deleted)
	e.blobs.mu.Unlock()
	if puts != 0 || deleted != 1 {
		t.Fatalf("chunks after kill %d, blobs deleted %d", puts, deleted)
	}
	recs, _ := e.te.st.ListFiles(ctx)
	if len(recs) != 1 || recs[0].State != store.FileFailed {
		t.Fatalf("records = %+v", recs)
	}

	// While killed, a new upload does not start.
	e.blobs.onPut = nil
	if _, err := e.files.SendFile(ctx, e.te.link, e.project, "big.bin", ""); !errors.Is(err, core.ErrKilled) {
		t.Fatalf("SendFile while killed err = %v", err)
	}
}

// Closing a link stops its running downloads: the file fails with
// link_closed, nothing is left on disk, and no file notice or chat follows.
func TestLinkCloseCancelsDownload(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	body := e.blobs.put(t, "big.bin", randomBytes(t, 3*core.FileChunkBytes))
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.blobs.onGet = func(n uint32) {
		if n == 1 {
			once.Do(func() { close(reached) })
			<-release
		}
	}
	env := d2Env(t, e.te.peer, core.KindFileOffer, e.te.link.ID, body)
	g := d2Gated(e.te.st, e.te.shared, e.te.replies, HandlerFunc(e.files.HandleOffer), HandlerFunc(e.files.RejectOffer))
	if err := g.Handle(ctx, e.te.peer, env); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	chats := len(e.te.sender.ofKind(core.KindChat))
	if err := e.te.links.Disconnect(ctx, "", e.te.link.Num); err != nil {
		t.Fatal(err)
	}
	close(release)
	e.files.Wait()
	r := e.record(t, body.FileID)
	if r.State != store.FileFailed || r.Reason != ReasonLinkClosed {
		t.Fatalf("download after the link closed: %s (%s)", r.State, r.Reason)
	}
	for _, p := range []string{r.LocalPath, r.LocalPath + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s left behind (%v)", p, err)
		}
	}
	items, _ := e.te.inbox.Check(ctx, e.te.session.ID, 10)
	for _, it := range items {
		if it.Kind == "file" || it.FileID != "" {
			t.Errorf("file notice after the link closed: %+v", it)
		}
	}
	if got := len(e.te.sender.ofKind(core.KindChat)); got != chats {
		t.Fatalf("%d chats sent on the closed link", got-chats)
	}
}

// A held file declined because its link closed gets a local notice, but no
// chat goes out on the closed link.
func TestDeclineOnClosedLinkSendsNoChat(t *testing.T) {
	ctx := context.Background()
	e := d2FileSvc(t, 0)
	body := e.blobs.put(t, "readme.md", []byte("hello"))
	e.hold(t, body.FileID, body)
	if err := e.te.links.Disconnect(ctx, "", e.te.link.Num); err != nil {
		t.Fatal(err)
	}
	if r := e.record(t, body.FileID); r.State != store.FileDeclined || r.Reason != ReasonLinkClosed {
		t.Fatalf("held file after the link closed: %s (%s)", r.State, r.Reason)
	}
	if got := e.te.sender.ofKind(core.KindChat); len(got) != 0 {
		t.Fatalf("chat sent on the closed link: %+v", got)
	}
}
