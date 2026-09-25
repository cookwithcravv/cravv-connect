package core

import (
	"sync"
	"testing"
	"time"
)

func TestFakeClockAdvance(t *testing.T) {
	start := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	c := NewFakeClock(start)
	if !c.Now().Equal(start) {
		t.Fatalf("Now() = %v, want %v", c.Now(), start)
	}
	c.Advance(90 * time.Second)
	if want := start.Add(90 * time.Second); !c.Now().Equal(want) {
		t.Fatalf("after Advance, Now() = %v, want %v", c.Now(), want)
	}
}

func TestFakeClockConcurrent(t *testing.T) {
	c := NewFakeClock(time.UnixMilli(0))
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); c.Advance(time.Millisecond) }()
		go func() { defer wg.Done(); _ = c.Now() }()
	}
	wg.Wait()
	if got := c.Now().UnixMilli(); got != 50 {
		t.Fatalf("Now() = %d ms, want 50", got)
	}
}

func TestSystemClockMoves(t *testing.T) {
	var c Clock = SystemClock{}
	if c.Now().IsZero() {
		t.Fatal("SystemClock returned zero time")
	}
}
