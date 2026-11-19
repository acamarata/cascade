// Package monitor provides stall monitoring primitives.
package monitor

import (
	"sync"
	"testing"
	"time"
)

type fakeClockLC struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimerLC
}

type fakeTimerLC struct {
	due time.Time
	f   func()
}

func (f *fakeClockLC) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClockLC) AfterFunc(d time.Duration, cb func()) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	due := f.now.Add(d)
	t := &fakeTimerLC{due: due, f: cb}
	f.timers = append(f.timers, t)
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		for i, ft := range f.timers {
			if ft == t {
				f.timers = append(f.timers[:i], f.timers[i+1:]...)
				break
			}
		}
	}
}

func (f *fakeClockLC) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	for {
		var earliest *fakeTimerLC
		var earliestIdx int
		for i, t := range f.timers {
			if !t.due.After(target) {
				if earliest == nil || t.due.Before(earliest.due) {
					earliest = t
					earliestIdx = i
				}
			}
		}
		if earliest == nil {
			break
		}
		f.now = earliest.due
		f.timers = append(f.timers[:earliestIdx], f.timers[earliestIdx+1:]...)
		cb := earliest.f
		f.mu.Unlock()
		cb()
		f.mu.Lock()
	}
	f.now = target
	f.mu.Unlock()
}

// TestTicksEmittedAtInterval verifies that ticks are emitted at the correct interval.
func TestTicksEmittedAtInterval(t *testing.T) {
	origin := time.Now()
	fc := &fakeClockLC{now: origin}
	var mu sync.Mutex
	var ticks []Tick
	capture := func(tk Tick) {
		mu.Lock()
		ticks = append(ticks, tk)
		mu.Unlock()
	}
	em, err := NewEmitter(fc, 10*time.Second, capture)
	if err != nil {
		t.Fatalf("NewEmitter: %v", err)
	}

	// Dormant before start
	mu.Lock()
	if len(ticks) != 0 {
		t.Errorf("expected 0 ticks before start, got %d", len(ticks))
	}
	mu.Unlock()

	if err := em.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fc.Advance(30 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(ticks) != 3 {
		t.Fatalf("expected 3 ticks, got %d", len(ticks))
	}
	for i, tk := range ticks {
		exp := origin.Add(time.Duration(i+1) * 10 * time.Second)
		if !tk.At.Equal(exp) {
			t.Errorf("tick %d at %v, want %v", i, tk.At, exp)
		}
	}

	// Zero-value emitter Start returns ErrInvalidMonitorInput
	var zero Emitter
	if err := zero.Start(); err != ErrInvalidMonitorInput {
		t.Errorf("zero-value Start error = %v, want %v", err, ErrInvalidMonitorInput)
	}
}

// TestSequenceIncreases verifies that tick sequence numbers increase by 1.
func TestSequenceIncreases(t *testing.T) {
	fc := &fakeClockLC{now: time.Now()}
	var mu sync.Mutex
	var ticks []Tick
	capture := func(tk Tick) {
		mu.Lock()
		ticks = append(ticks, tk)
		mu.Unlock()
	}
	em, err := NewEmitter(fc, 10*time.Second, capture)
	if err != nil {
		t.Fatalf("NewEmitter: %v", err)
	}
	if err := em.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fc.Advance(30 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	if len(ticks) != 3 {
		t.Fatalf("expected 3 ticks, got %d", len(ticks))
	}
	for i, tk := range ticks {
		if tk.Seq != uint64(i+1) {
			t.Errorf("tick %d seq = %d, want %d", i, tk.Seq, i+1)
		}
	}
}

// TestStopEndsTicks verifies that Stop prevents further ticks.
func TestStopEndsTicks(t *testing.T) {
	fc := &fakeClockLC{now: time.Now()}
	var mu sync.Mutex
	var ticks []Tick
	capture := func(tk Tick) {
		mu.Lock()
		ticks = append(ticks, tk)
		mu.Unlock()
	}
	em, err := NewEmitter(fc, 10*time.Second, capture)
	if err != nil {
		t.Fatalf("NewEmitter: %v", err)
	}
	if err := em.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fc.Advance(15 * time.Second)
	mu.Lock()
	count1 := len(ticks)
	mu.Unlock()
	if count1 != 1 {
		t.Fatalf("expected 1 tick before stop, got %d", count1)
	}

	em.Stop()
	fc.Advance(30 * time.Second)
	mu.Lock()
	count2 := len(ticks)
	mu.Unlock()
	if count2 != count1 {
		t.Errorf("expected %d ticks after stop, got %d", count1, count2)
	}

	// Later Start returns ErrInvalidMonitorInput
	if err := em.Start(); err != ErrInvalidMonitorInput {
		t.Errorf("post-Stop Start error = %v, want %v", err, ErrInvalidMonitorInput)
	}

	// Nil receiver Stop is no-op
	var nilEm *Emitter
	nilEm.Stop()

	// Zero-value receiver Stop is no-op
	var zeroEm Emitter
	zeroEm.Stop()
}
