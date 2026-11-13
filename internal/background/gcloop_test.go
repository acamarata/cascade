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
