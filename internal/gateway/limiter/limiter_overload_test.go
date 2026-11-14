package limiter

import (
	"sync"
	"testing"
	"time"
)

// testClock implements Clock for testing with manual time control.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) NewTimer(_ time.Duration) Timer {
	return &testTimer{ch: make(chan time.Time, 1), fired: false}
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type testTimer struct {
	ch    chan time.Time
	fired bool
}

func (t *testTimer) C() <-chan time.Time { return t.ch }
func (t *testTimer) Stop() bool          { return true }

func TestOverloadHalvesRate(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &testClock{now: base}

	l, err := New(60, clk)
	if err != nil {
		t.Fatalf("New(60): %v", err)
	}

	l.OnOverload()
	if got := l.Rate(); got != 30 {
		t.Fatalf("after OnOverload from 60: got rate %d, want 30", got)
	}

	l2, err := New(1, clk)
	if err != nil {
		t.Fatalf("New(1): %v", err)
	}

	l2.OnOverload()
	if got := l2.Rate(); got != 1 {
		t.Fatalf("after OnOverload from 1: got rate %d, want 1", got)
	}
}

func TestHalvingDebounced30s(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &testClock{now: base}

	l, err := New(60, clk)
	if err != nil {
		t.Fatalf("New(60): %v", err)
	}

	// First overload at t0: halves 60 -> 30
	l.OnOverload()
	if got := l.Rate(); got != 30 {
		t.Fatalf("after first OnOverload: got rate %d, want 30", got)
	}

	// Advance 10s, second overload: debounced, rate stays 30
	clk.Advance(10 * time.Second)
	l.OnOverload()
	if got := l.Rate(); got != 30 {
		t.Fatalf("after debounced OnOverload at +10s: got rate %d, want 30", got)
	}

	// Recovery anchor should have moved to +10s
	// Advance to exactly 30s from base (i.e., 20s from last halving at t0)
	// which should still be debounced (< 30s from last accepted halving at t0)
	clk.Advance(20 * time.Second)
	l.OnOverload()
	if got := l.Rate(); got != 30 {
		t.Fatalf("after OnOverload at +30s (20s from last halving): got rate %d, want 30", got)
	}
}

func TestHalvesAgainAfter30s(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &testClock{now: base}

	l, err := New(60, clk)
	if err != nil {
		t.Fatalf("New(60): %v", err)
	}

	// First overload at t0: halves 60 -> 30
	l.OnOverload()
	if got := l.Rate(); got != 30 {
		t.Fatalf("after first OnOverload: got rate %d, want 30", got)
	}

	// Advance exactly 30s: should halve again 30 -> 15
	clk.Advance(30 * time.Second)
	l.OnOverload()
	if got := l.Rate(); got != 15 {
		t.Fatalf("after OnOverload at +30s: got rate %d, want 15", got)
	}

	// Now test +29s debounce: advance only 29s, should be debounced
	clk.Advance(29 * time.Second)
	l.OnOverload()
	if got := l.Rate(); got != 15 {
		t.Fatalf("after debounced OnOverload at +29s from last halving: got rate %d, want 15", got)
	}
}
