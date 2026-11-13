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

// TestBucketAllowsWithinRate verifies that TakeStreamTokens correctly refills
// tokens based on elapsed time, allows requests within capacity, and handles
// edge cases like subsecond remainders and zero requests.
func TestBucketAllowsWithinRate(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

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

	t0_500ms := t0.Add(500 * time.Millisecond)
	updated, allowed, err = TakeStreamTokens(updated, 0, t0_500ms)
	if err != nil {
		t.Fatalf("unexpected error at t0+500ms: %v", err)
	}
	if allowed != 0 {
		t.Fatalf("expected 0 allowed at t0+500ms, got %d", allowed)
	}
	if updated.Tokens != 5 {
		t.Fatalf("expected 5 tokens at t0+500ms, got %d", updated.Tokens)
	}

	t0_1s := t0.Add(1 * time.Second)
	updated, allowed, err = TakeStreamTokens(updated, 0, t0_1s)
	if err != nil {
		t.Fatalf("unexpected error at t0+1s: %v", err)
	}
	if allowed != 0 {
		t.Fatalf("expected 0 allowed at t0+1s (n=0), got %d", allowed)
	}
	if updated.Tokens != 10 {
		t.Fatalf("expected 10 tokens after refill at t0+1s, got %d", updated.Tokens)
	}
	if !updated.Last.Equal(t0_1s) {
		t.Fatalf("expected Last to advance to t0+1s, got %v", updated.Last)
	}

	t0_1s_500ms := t0.Add(1*time.Second + 500*time.Millisecond)
	updated, allowed, err = TakeStreamTokens(updated, 3, t0_1s_500ms)
	if err != nil {
		t.Fatalf("unexpected error at t0+1s+500ms: %v", err)
	}
	if allowed != 3 {
		t.Fatalf("expected 3 allowed at t0+1s+500ms, got %d", allowed)
	}
	if updated.Tokens != 7 {
		t.Fatalf("expected 7 tokens remaining at t0+1s+500ms, got %d", updated.Tokens)
	}

	t0_2s_500ms := t0.Add(2*time.Second + 500*time.Millisecond)
	updated, allowed, err = TakeStreamTokens(updated, 0, t0_2s_500ms)
	if err != nil {
		t.Fatalf("unexpected error at t0+2s+500ms: %v", err)
	}
	if allowed != 0 {
		t.Fatalf("expected 0 allowed at t0+2s+500ms, got %d", allowed)
	}
	if updated.Tokens != 10 {
		t.Fatalf("expected 10 tokens after second refill, got %d", updated.Tokens)
	}
	expectedLast := t0_1s.Add(1 * time.Second)
	if !updated.Last.Equal(expectedLast) {
		t.Fatalf("expected Last at %v, got %v", expectedLast, updated.Last)
	}

	updated, allowed, err = TakeStreamTokens(updated, 0, t0_2s_500ms.Add(1*time.Second))
	if err != nil {
		t.Fatalf("unexpected error for n=0: %v", err)
	}
	if allowed != 0 {
		t.Fatalf("expected 0 allowed for n=0, got %d", allowed)
	}
	if updated.Tokens != 10 {
		t.Fatalf("expected 10 tokens after n=0 refill, got %d", updated.Tokens)
	}

	b2, _ := NewStreamBucket(10, 10, t0)
	original := b2
	_, _, err = TakeStreamTokens(b2, 5, t0.Add(-1*time.Second))
	if err == nil {
		t.Fatal("expected error for backward clock")
	}
	if !errors.Is(err, ErrInvalidStreamInput) {
		t.Fatalf("expected ErrInvalidStreamInput for backward clock, got %v", err)
	}
	if b2 != original {
		t.Fatalf("bucket should not be mutated on backward clock")
	}

	uninitialized := StreamBucket{Capacity: 10, Rate: 10, Tokens: 10, Last: t0, Initialized: false}
	_, _, err = TakeStreamTokens(uninitialized, 5, t0)
	if err == nil {
		t.Fatal("expected error for uninitialized bucket")
	}
	if !errors.Is(err, ErrInvalidStreamInput) {
		t.Fatalf("expected ErrInvalidStreamInput for uninitialized bucket, got %v", err)
	}

	invalidTokens := StreamBucket{Capacity: 10, Rate: 10, Tokens: 15, Last: t0, Initialized: true}
	_, _, err = TakeStreamTokens(invalidTokens, 5, t0)
	if err == nil {
		t.Fatal("expected error for tokens > capacity")
	}
	if !errors.Is(err, ErrInvalidStreamInput) {
		t.Fatalf("expected ErrInvalidStreamInput for invalid tokens, got %v", err)
	}

	invalidTokens2 := StreamBucket{Capacity: 10, Rate: 10, Tokens: -1, Last: t0, Initialized: true}
	_, _, err = TakeStreamTokens(invalidTokens2, 5, t0)
	if err == nil {
		t.Fatal("expected error for negative tokens")
	}
	if !errors.Is(err, ErrInvalidStreamInput) {
		t.Fatalf("expected ErrInvalidStreamInput for negative tokens, got %v", err)
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
