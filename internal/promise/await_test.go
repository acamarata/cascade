package promise

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDeclarationsCompile verifies that all exported symbols compile and
// that the stub functions return ErrNotImplemented (operation unavailable).
func TestDeclarationsCompile(t *testing.T) {
	if ErrNotImplemented == nil {
		t.Fatal("ErrNotImplemented must be non-nil")
	}
	if ErrNotImplemented.Error() != "promise: operation unavailable" {
		t.Fatalf("unexpected error string: %s", ErrNotImplemented.Error())
	}

	if defaultMaxAttempts != 3 {
		t.Fatalf("defaultMaxAttempts = %d, want 3", defaultMaxAttempts)
	}
	if defaultBackoff != 30*time.Second {
		t.Fatalf("defaultBackoff = %v, want 30s", defaultBackoff)
	}

	rp := RetryPolicy{MaxAttempts: 5, Backoff: time.Second}
	if rp.MaxAttempts != 5 || rp.Backoff != time.Second {
		t.Fatal("RetryPolicy fields failed")
	}

	ctx := context.Background()

	p, err := Await(ctx, "id1", time.Second)
	if p != nil || !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Await returned (%v, %v), want (nil, ErrNotImplemented)", p, err)
	}

	err = RecoverStale(ctx, nil)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("RecoverStale returned %v, want ErrNotImplemented", err)
	}

	p2, err := Do(ctx, nil, nil, time.Second)
	if p2 != nil || !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Do returned (%v, %v), want (nil, ErrNotImplemented)", p2, err)
	}
}
