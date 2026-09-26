package daemon

import (
	"errors"
	"regexp"
	"strings"
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
	if err := c.Show("link-1", "cravv-connect: link request from gpu-box/trainer asking tasks-ask."); err != nil {
		t.Fatal(err)
	}
	code := desk.shownCode(t)
	if texts := desk.all(); !strings.HasSuffix(texts[0], "Code "+code) {
		t.Fatalf("text %q", texts[0])
	}
	if err := c.Show("link-1", "again"); err != nil || desk.shownCode(t) != code {
		t.Fatalf("an unexpired code must be shown again, not replaced: %v", err)
	}
	if err := c.Check("link-2", code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("another item's code: %v", err)
	}
	if err := c.Check("link-1", code); err != nil {
		t.Fatalf("right code: %v", err)
	}
	if err := c.Check("link-1", code); !errors.Is(err, ErrBadCode) {
		t.Fatalf("a code is single-use: %v", err)
	}
	if err := c.Show("link-1", "x"); err != nil {
		t.Fatal(err)
	}
	clock.Advance(CodeValidity)
	if err := c.Check("link-1", desk.shownCode(t)); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired code: %v", err)
	}
}

func TestConfirmCodeLocksAfterThreeWrongTries(t *testing.T) {
	clock := core.NewFakeClock(d2Epoch)
	desk := &titleDesktop{}
	c := NewConfirmCodes(clock, desk)
	c.rand = func() (int, error) { return 7, nil }
	if err := c.Show("task-x", "t"); err != nil {
		t.Fatal(err)
	}
	if code := desk.shownCode(t); code != "0007" {
		t.Fatalf("code %q, want 4 digits with leading zeros", code)
	}
	for range CodeMaxWrong {
		if err := c.Check("task-x", "1234"); !errors.Is(err, ErrBadCode) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	if err := c.Check("task-x", "0007"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("the right code after 3 wrong tries: %v", err)
	}
	if err := c.Show("task-x", "t"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("no new code while locked: %v", err)
	}
	clock.Advance(CodeValidity + time.Second)
	c.rand = func() (int, error) { return 4821, nil }
	if err := c.Show("task-x", "t"); err != nil || desk.shownCode(t) != "4821" {
		t.Fatalf("a fresh code after expiry: %v", err)
	}
	if err := c.Check("task-x", "4821"); err != nil {
		t.Fatal(err)
	}
}

func TestConfirmCodeNeedsADesktop(t *testing.T) {
	c := NewConfirmCodes(core.NewFakeClock(d2Epoch), nopDesktop{})
	if err := c.Show("link-1", "x"); !errors.Is(err, ErrNoDesktop) {
		t.Fatalf("headless: %v", err)
	}
	if DesktopAvailable(nil) || !DesktopAvailable(&d2Desktop{}) || !DesktopAvailable(osascriptNotifier{}) {
		t.Fatal("DesktopAvailable")
	}
}
