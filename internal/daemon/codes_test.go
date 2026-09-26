package daemon

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cravv/cravv-connect/internal/core"
)

// titleDesktop records notification titles and texts.
type titleDesktop struct {
	d2Desktop
	titles []string
}

func (d *titleDesktop) Notify(title, text string) {
	d.mu.Lock()
	d.titles = append(d.titles, title)
	d.mu.Unlock()
	d.d2Desktop.Notify(title, text)
}

// shownCode reads the code from the newest notification title.
func (d *titleDesktop) shownCode(t *testing.T) string {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.titles) == 0 {
		t.Fatal("no notification shown")
	}
	m := regexp.MustCompile(`^cravv-connect code (\d{4})$`).FindStringSubmatch(d.titles[len(d.titles)-1])
	if m == nil {
		t.Fatalf("title %q", d.titles[len(d.titles)-1])
	}
	return m[1]
}

func TestConfirmCodeSingleUseAndExpiry(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	desk := &titleDesktop{}
	c := NewConfirmCodes(clock, desk)
	if err := c.Show("s1", "link-1", "cravv-connect: link request from gpu-box/trainer asking tasks-ask."); err != nil {
		t.Fatal(err)
	}
	code := desk.shownCode(t)
	if texts := desk.all(); !strings.HasSuffix(texts[0], "Code "+code) {
		t.Fatalf("text %q", texts[0])
	}
	if err := c.Show("s1", "link-1", "again"); err != nil || desk.shownCode(t) != code {
		t.Fatalf("an unexpired code must be shown again, not replaced: %v", err)
	}
	if err := c.Check("s1", "link-2", code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("another item's code: %v", err)
	}
	if err := c.Check("s1", "link-1", code); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if err := c.Check("s1", "link-1", code); err != nil {
		t.Fatalf("a right code stays valid until the decision is applied: %v", err)
	}
	c.Forget("link-1")
	if err := c.Check("s1", "link-1", code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("a code is single-use: %v", err)
	}
	if err := c.Show("s1", "link-1", "x"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(CodeValidity)
	if err := c.Check("s1", "link-1", desk.shownCode(t)); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired code: %v", err)
	}
}

// Review focus: wrong tries count per item over its whole life, across
// code rotations. After CodeMaxWrong the item never gets a code again: the
// human decides it with their password.
func TestConfirmCodeLocksAnItemForGood(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	desk := &titleDesktop{}
	c := NewConfirmCodes(clock, desk)
	c.rand = func() (int, error) { return 7, nil }
	if err := c.Show("s1", "task-x", "t"); err != nil {
		t.Fatal(err)
	}
	if code := desk.shownCode(t); code != "0007" {
		t.Fatalf("code %q, want 4 digits with leading zeros", code)
	}
	for range CodeMaxWrong - 1 {
		if err := c.Check("s1", "task-x", "1234"); !errors.Is(err, ErrBadCode) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	// A fresh code after expiry does not reset the count.
	clock.Advance(CodeValidity + time.Second)
	c.rand = func() (int, error) { return 4821, nil }
	if err := c.Show("s1", "task-x", "t"); err != nil || desk.shownCode(t) != "4821" {
		t.Fatalf("a fresh code after expiry: %v", err)
	}
	if err := c.Check("s1", "task-x", "1234"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("wrong code: %v", err)
	}
	if err := c.Check("s1", "task-x", "4821"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("the right code after %d wrong tries over two codes: %v", CodeMaxWrong, err)
	}
	if err := c.Show("s1", "task-x", "t"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("no new code while locked: %v", err)
	}
	clock.Advance(7 * 24 * time.Hour)
	if err := c.Show("s1", "task-x", "t"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("the lock must not run out: %v", err)
	}
	if err := c.Check("s1", "task-x", "4821"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("the lock must not run out: %v", err)
	}
	// Other items are not affected.
	if err := c.Show("s1", "task-y", "t"); err != nil {
		t.Fatal(err)
	}
	if err := c.Check("s1", "task-y", "4821"); err != nil {
		t.Fatal(err)
	}
}

// Review focus: a session gets at most CodeSessionMaxWrong wrong codes in
// CodeSessionWindow over all its items, so rotating items cannot be used
// to keep guessing.
func TestConfirmCodeSessionCap(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	desk := &titleDesktop{}
	c := NewConfirmCodes(clock, desk)
	c.rand = func() (int, error) { return 4821, nil }
	for i := range CodeSessionMaxWrong {
		item := "link-" + itoa(int64(i/(CodeMaxWrong-1)))
		if err := c.Show("s1", item, "t"); err != nil {
			t.Fatalf("show %s: %v", item, err)
		}
		if err := c.Check("s1", item, "0000"); !errors.Is(err, ErrBadCode) {
			t.Fatalf("wrong code %d: %v", i+1, err)
		}
	}
	if err := c.Show("s1", "link-99", "t"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("a code for a capped session: %v", err)
	}
	if err := c.Check("s1", "link-0", "4821"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("the right code in a capped session: %v", err)
	}
	if err := c.Show("s2", "link-50", "t"); err != nil {
		t.Fatalf("another session: %v", err)
	}
	if err := c.Check("s2", "link-50", "4821"); err != nil {
		t.Fatalf("another session: %v", err)
	}
	clock.Advance(CodeSessionWindow)
	if err := c.Show("s1", "link-99", "t"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	if err := c.Check("s1", "link-99", "4821"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
}

// Expired codes are dropped by Show and Check, not kept forever.
func TestConfirmCodePrunesExpired(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	c := NewConfirmCodes(clock, &titleDesktop{})
	live := func() int {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.codes)
	}
	for _, item := range []string{"link-1", "link-2", "link-3"} {
		if err := c.Show("s1", item, "t"); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(CodeValidity)
	if err := c.Show("s1", "link-4", "t"); err != nil || live() != 1 {
		t.Fatalf("Show kept %d codes (%v)", live(), err)
	}
	clock.Advance(CodeValidity)
	if err := c.Check("s1", "link-9", "0000"); !errors.Is(err, ErrBadCode) || live() != 0 {
		t.Fatalf("Check kept %d codes (%v)", live(), err)
	}
}

// mapSettings is an in-memory SettingsStore.
type mapSettings struct {
	mu   sync.Mutex
	m    map[string]string
	fail bool
}

func (s *mapSettings) GetSetting(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", false, errors.New("disk on fire")
	}
	v, ok := s.m[key]
	return v, ok, nil
}

func (s *mapSettings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]string{}
	}
	s.m[key] = value
	return nil
}

// Wrong tries survive a daemon restart; unreadable state fails closed.
func TestConfirmCodeWrongTriesArePersisted(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	st := &mapSettings{}
	c := NewConfirmCodes(clock, &titleDesktop{}, WithCodeState(st))
	c.rand = func() (int, error) { return 4821, nil }
	c.Show("s1", "task-x", "t")
	for range CodeMaxWrong {
		c.Check("s1", "task-x", "0000")
	}
	c2 := NewConfirmCodes(clock, &titleDesktop{}, WithCodeState(st))
	if err := c2.Show("s1", "task-x", "t"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("a restart unlocked the item: %v", err)
	}
	if err := c2.Show("s1", "task-y", "t"); err != nil {
		t.Fatalf("another item after a restart: %v", err)
	}
	st.fail = true
	c3 := NewConfirmCodes(clock, &titleDesktop{}, WithCodeState(st))
	if err := c3.Show("s1", "task-y", "t"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("unreadable state: %v", err)
	}
	clock.Advance(CodeSessionWindow)
	if err := c3.Show("s1", "task-y", "t"); err != nil {
		t.Fatalf("the fail-closed lock lasts one window: %v", err)
	}
}

func TestConfirmCodeNeedsADesktop(t *testing.T) {
	c := NewConfirmCodes(core.NewFakeClock(d2Epoch), nopDesktop{})
	if err := c.Show("s1", "link-1", "x"); !errors.Is(err, ErrNoDesktop) {
		t.Fatalf("headless: %v", err)
	}
	if DesktopAvailable(nil) || !DesktopAvailable(&d2Desktop{}) || !DesktopAvailable(osascriptNotifier{}) {
		t.Fatal("DesktopAvailable")
	}
}
