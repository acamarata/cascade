package promise

import (
	"testing"
	"time"
)

// TestRetryDefaults checks the default retry limits and the RetryPolicy fields.
func TestRetryDefaults(t *testing.T) {
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
}
