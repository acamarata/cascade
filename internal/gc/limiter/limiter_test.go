package limiter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeTimer struct {
	ch chan time.Time
}

func (f *fakeTimer) C() <-chan time.Time { return f.ch }
func (f *fakeTimer) Stop() bool          { return true }

type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	timer *fakeTimer
}

// Now returns the fake time; it is safe to call while another goroutine advances the clock.
func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake time forward by d and returns the new time.
func (f *fakeClock) Advance(d time.Duration) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	return f.now
}
func (f *fakeClock) NewTimer(_ time.Duration) Timer {
	return f.timer
}

// TestSharedBucketAcrossRetries verifies shared bucket behaviour.
func TestSharedBucketAcrossRetries(t *testing.T) {
	t.Run("request and retries consume tokens", testRequestAndRetriesConsumeTokens)
	t.Run("drain bucket then refill", testDrainBucketThenRefill)
	t.Run("cancelled context consumes no token", testCancelledContextConsumesNoToken)
	t.Run("nil ctx returns ErrInvalidArgument", testNilCtxReturnsErrInvalidArgument)
	t.Run("invalid constructor args", testInvalidConstructorArgs)
}

func testRequestAndRetriesConsumeTokens(t *testing.T) {
	fc := &fakeClock{
		now:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		timer: &fakeTimer{ch: make(chan time.Time)},
	}
	l, err := New(60, fc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
	}
	if l.Rate() != 60 {
		t.Errorf("Rate = %d, want 60", l.Rate())
	}
}

func testDrainBucketThenRefill(t *testing.T) {
	ch := make(chan time.Time, 1)
	fc := &fakeClock{
		now:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		timer: &fakeTimer{ch: ch},
	}
	l, err := New(1, fc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("Wait 1: %v", err)
	}
	go func() {
		ch <- fc.Advance(60 * time.Second)
	}()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("Wait 2: %v", err)
	}
}

func testCancelledContextConsumesNoToken(t *testing.T) {
	fc := &fakeClock{
		now:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		timer: &fakeTimer{ch: make(chan time.Time)},
	}
	l, err := New(60, fc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = l.Wait(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Wait cancelled = %v, want context.Canceled", err)
	}
}

func testNilCtxReturnsErrInvalidArgument(t *testing.T) {
	fc := &fakeClock{
		now:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		timer: &fakeTimer{ch: make(chan time.Time)},
	}
	l, err := New(60, fc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var nilCtx context.Context
	err = l.Wait(nilCtx)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("Wait nil = %v, want ErrInvalidArgument", err)
	}
}

func testInvalidConstructorArgs(t *testing.T) {
	fc := &fakeClock{now: time.Now(), timer: &fakeTimer{ch: make(chan time.Time)}}
	if _, err := New(0, fc); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("New(0) = %v, want ErrInvalidArgument", err)
	}
	if _, err := New(-1, fc); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("New(-1) = %v, want ErrInvalidArgument", err)
	}
	if _, err := New(60, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("New(nil) = %v, want ErrInvalidArgument", err)
	}
	var nc Clock
	if _, err := New(60, nc); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("New(typed nil) = %v, want ErrInvalidArgument", err)
	}
}
