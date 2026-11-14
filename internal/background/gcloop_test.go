package background

import (
	"errors"
	"testing"
	"time"
)

// TestLimiterRejectsZeroCapacity ensures that invalid capacity or rate values
// return the appropriate sentinel error and a zero bucket.
func TestLimiterRejectsZeroCapacity(t *testing.T) {
	now := time.Now()

	// Test capacity 0
	b, err := NewStreamBucket(0, 1, now)
	if err == nil {
		t.Fatal("expected error for capacity 0")
	}
	if !errors.Is(err, ErrInvalidCapacity) {
		t.Fatalf("expected ErrInvalidCapacity, got %v", err)
	}
	if err.Error() != "E_INVALID_CAPACITY" {
		t.Fatalf("expected error text 'E_INVALID_CAPACITY', got %q", err.Error())
	}
	if b.Capacity != 0 || b.Rate != 0 || b.Tokens != 0 || b.Initialized {
		t.Fatalf("expected zero bucket for capacity 0, got %+v", b)
	}

	// Test negative capacity
	b, err = NewStreamBucket(-1, 1, now)
	if err == nil {
		t.Fatal("expected error for negative capacity")
	}
	if !errors.Is(err, ErrInvalidCapacity) {
		t.Fatalf("expected ErrInvalidCapacity for negative capacity, got %v", err)
	}
	if b.Capacity != 0 || b.Rate != 0 || b.Tokens != 0 || b.Initialized {
		t.Fatalf("expected zero bucket for negative capacity, got %+v", b)
	}

	// Test nonpositive rate
	b, err = NewStreamBucket(1, 0, now)
	if err == nil {
		t.Fatal("expected error for rate 0")
	}
	if !errors.Is(err, ErrInvalidStreamInput) {
		t.Fatalf("expected ErrInvalidStreamInput for rate 0, got %v", err)
	}
	if b.Capacity != 0 || b.Rate != 0 || b.Tokens != 0 || b.Initialized {
		t.Fatalf("expected zero bucket for rate 0, got %+v", b)
	}

	b, err = NewStreamBucket(1, -1, now)
	if err == nil {
		t.Fatal("expected error for negative rate")
	}
	if !errors.Is(err, ErrInvalidStreamInput) {
		t.Fatalf("expected ErrInvalidStreamInput for negative rate, got %v", err)
	}
	if b.Capacity != 0 || b.Rate != 0 || b.Tokens != 0 || b.Initialized {
		t.Fatalf("expected zero bucket for negative rate, got %+v", b)
	}
}

// takeInitialFiveTokens builds a 10/10 bucket at t0, checks it starts full and
// takes 5 tokens at t0, returning the updated bucket.
func takeInitialFiveTokens(t *testing.T, t0 time.Time) StreamBucket {
	t.Helper()
	b, err := NewStreamBucket(10, 10, t0)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}
	if b.Tokens != 10 {
		t.Fatalf("expected initial tokens 10, got %d", b.Tokens)
	}
	updated, allowed, err := TakeStreamTokens(b, 5, t0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed != 5 {
		t.Fatalf("expected 5 allowed, got %d", allowed)
	}
	if updated.Tokens != 5 {
		t.Fatalf("expected 5 tokens remaining, got %d", updated.Tokens)
	}
	return updated
}

// TestBucketAllowsWithinRate verifies that TakeStreamTokens correctly refills
// tokens based on elapsed time, allows requests within capacity, and handles
// edge cases like subsecond remainders and zero requests.
func TestBucketAllowsWithinRate(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	updated := takeInitialFiveTokens(t, t0)
	var err error
	var allowed int

	steps := []struct {
		name        string
		at          time.Duration
		n           int
		wantAllowed int
		wantTokens  int
		wantLast    time.Duration // checked only when checkLast is set
		checkLast   bool
	}{
		{name: "t0+500ms", at: 500 * time.Millisecond, n: 0, wantAllowed: 0, wantTokens: 5},
		{name: "t0+1s", at: time.Second, n: 0, wantAllowed: 0, wantTokens: 10, wantLast: time.Second, checkLast: true},
		{name: "t0+1s+500ms", at: time.Second + 500*time.Millisecond, n: 3, wantAllowed: 3, wantTokens: 7},
		{name: "t0+2s+500ms", at: 2*time.Second + 500*time.Millisecond, n: 0, wantAllowed: 0, wantTokens: 10, wantLast: 2 * time.Second, checkLast: true},
		{name: "t0+3s+500ms", at: 3*time.Second + 500*time.Millisecond, n: 0, wantAllowed: 0, wantTokens: 10},
	}
	for _, s := range steps {
		updated, allowed, err = TakeStreamTokens(updated, s.n, t0.Add(s.at))
		if err != nil {
			t.Fatalf("unexpected error at %s: %v", s.name, err)
		}
		if allowed != s.wantAllowed {
			t.Fatalf("expected %d allowed at %s, got %d", s.wantAllowed, s.name, allowed)
		}
		if updated.Tokens != s.wantTokens {
			t.Fatalf("expected %d tokens at %s, got %d", s.wantTokens, s.name, updated.Tokens)
		}
		if s.checkLast && !updated.Last.Equal(t0.Add(s.wantLast)) {
			t.Fatalf("expected Last at t0+%v at %s, got %v", s.wantLast, s.name, updated.Last)
		}
	}
}

// TestBucketRejectsInvalidState verifies that TakeStreamTokens rejects a
// backward clock and invalid bucket states without mutating the bucket.
func TestBucketRejectsInvalidState(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	b2, _ := NewStreamBucket(10, 10, t0)
	original := b2
	_, _, err := TakeStreamTokens(b2, 5, t0.Add(-1*time.Second))
	if err == nil {
		t.Fatal("expected error for backward clock")
	}
	if !errors.Is(err, ErrInvalidStreamInput) {
		t.Fatalf("expected ErrInvalidStreamInput for backward clock, got %v", err)
	}
	if b2 != original {
		t.Fatalf("bucket should not be mutated on backward clock")
	}

	cases := []struct {
		name   string
		bucket StreamBucket
	}{
		{"uninitialized bucket", StreamBucket{Capacity: 10, Rate: 10, Tokens: 10, Last: t0, Initialized: false}},
		{"tokens > capacity", StreamBucket{Capacity: 10, Rate: 10, Tokens: 15, Last: t0, Initialized: true}},
		{"negative tokens", StreamBucket{Capacity: 10, Rate: 10, Tokens: -1, Last: t0, Initialized: true}},
	}
	for _, c := range cases {
		_, _, err = TakeStreamTokens(c.bucket, 5, t0)
		if err == nil {
			t.Fatalf("expected error for %s", c.name)
		}
		if !errors.Is(err, ErrInvalidStreamInput) {
			t.Fatalf("expected ErrInvalidStreamInput for %s, got %v", c.name, err)
		}
	}
}

// TestBucketBlocksExcessTokens verifies that requests exceeding available tokens
// are rate-limited, partially fulfilled, and return the appropriate error.
func TestBucketBlocksExcessTokens(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	b, err := NewStreamBucket(10, 10, t0)
	if err != nil {
		t.Fatalf("failed to create bucket: %v", err)
	}

	updated, allowed, err := TakeStreamTokens(b, 15, t0)
	if err == nil {
		t.Fatal("expected ErrRateLimited for excess request")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
	if err.Error() != "E_RATE_LIMITED" {
		t.Fatalf("expected error text 'E_RATE_LIMITED', got %q", err.Error())
	}
	if allowed != 10 {
		t.Fatalf("expected 10 allowed (partial), got %d", allowed)
	}
	if updated.Tokens != 0 {
		t.Fatalf("expected 0 tokens remaining, got %d", updated.Tokens)
	}

	t0_1h := t0.Add(1 * time.Hour)
	updated, allowed, err = TakeStreamTokens(updated, 5, t0_1h)
	if err != nil {
		t.Fatalf("unexpected error after large interval: %v", err)
	}
	if allowed != 5 {
		t.Fatalf("expected 5 allowed after large interval, got %d", allowed)
	}
	if updated.Tokens != 5 {
		t.Fatalf("expected 5 tokens remaining, got %d", updated.Tokens)
	}

	b2, _ := NewStreamBucket(10, 1, t0)
	updated2, allowed2, err := TakeStreamTokens(b2, 5, t0.Add(1*time.Second))
	if err != nil {
		t.Fatalf("unexpected error for rate=1: %v", err)
	}
	if allowed2 != 5 {
		t.Fatalf("expected 5 allowed for rate=1 after 1s, got %d", allowed2)
	}
	if updated2.Tokens != 5 {
		t.Fatalf("expected 5 tokens (10 capped, minus 5), got %d", updated2.Tokens)
	}
}
