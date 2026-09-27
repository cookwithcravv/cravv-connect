package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cookwithcravv/cravv-connect/internal/core"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func TestFileLoggerAppendsJSONLWith0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	clock := core.NewFakeClock(t0)
	l := NewFileLogger(path, clock)
	if err := l.Record(Event{Type: EvPair, Peer: "abc", Alias: "gpu-box", Detail: map[string]any{"trust": "ask-first"}}); err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	explicit := t0.Add(-time.Hour)
	if err := l.Record(Event{TS: explicit, Type: EvKill}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), raw)
	}
	if !strings.Contains(lines[0], `"type":"pair"`) || !strings.Contains(lines[0], `"alias":"gpu-box"`) {
		t.Fatalf("line 0 = %s", lines[0])
	}
	if strings.Contains(lines[1], `"peer"`) || strings.Contains(lines[1], `"detail"`) {
		t.Fatalf("empty fields not omitted: %s", lines[1])
	}
	evs, err := ReadEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || !evs[0].TS.Equal(t0) || !evs[1].TS.Equal(explicit) {
		t.Fatalf("events = %+v", evs)
	}
	if evs[0].Detail["trust"] != "ask-first" {
		t.Fatalf("detail = %v", evs[0].Detail)
	}
}

func TestFileLoggerTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewFileLogger(path, core.NewFakeClock(t0)).Record(Event{Type: EvKill}); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestReadEventsLastN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l := NewFileLogger(path, core.NewFakeClock(t0))
	for i := 0; i < 25; i++ {
		if err := l.Record(Event{Type: EvTaskIn, ItemID: fmt.Sprintf("t%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		limit       int
		n           int
		first, last string
	}{
		{0, 25, "t00", "t24"},
		{3, 3, "t22", "t24"},
		{25, 25, "t00", "t24"},
		{100, 25, "t00", "t24"},
	}
	for _, c := range cases {
		evs, err := ReadEvents(path, c.limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != c.n || evs[0].ItemID != c.first || evs[len(evs)-1].ItemID != c.last {
			t.Errorf("limit %d: n=%d first=%s last=%s", c.limit, len(evs), evs[0].ItemID, evs[len(evs)-1].ItemID)
		}
	}
}

func TestReadEventsMissingFileAndCorruptLines(t *testing.T) {
	dir := t.TempDir()
	evs, err := ReadEvents(filepath.Join(dir, "nope.log"), 10)
	if err != nil || len(evs) != 0 {
		t.Fatalf("missing file = %v, %v", evs, err)
	}
	path := filepath.Join(dir, "audit.log")
	content := `{"ts":"2026-09-26T12:00:00Z","type":"pair"}` + "\n" + `not json` + "\n" + `{"ts":"2026-09-26T12:01:00Z","type":"kill"}` + "\n" + `{"ts":"2026-09-26T12:0`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	evs, err = ReadEvents(path, 0)
	if err != nil || len(evs) != 2 || evs[0].Type != EvPair || evs[1].Type != EvKill {
		t.Fatalf("events = %+v, %v", evs, err)
	}
}

func TestFileLoggerConcurrentWritesStayWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	l := NewFileLogger(path, core.NewFakeClock(t0))
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := l.Record(Event{Type: EvFileIn, ItemID: fmt.Sprint(i), Hash: strings.Repeat("a", 64)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	evs, err := ReadEvents(path, 0)
	if err != nil || len(evs) != 50 {
		t.Fatalf("got %d events, %v; want 50", len(evs), err)
	}
}
