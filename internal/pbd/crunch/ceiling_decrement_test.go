package crunch

import (
	"errors"
	"sync"
	"testing"
)

// TestAuditRunDecrementsCounter verifies that DecrementAudit decrements the counter,
// clamps inputs via NewCounter, and is safe under concurrent use.
func TestAuditRunDecrementsCounter(t *testing.T) {
	c := NewCounter(150)
	got, err := c.DecrementAudit()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 149 {
		t.Fatalf("expected 149, got %d", got)
	}

	tests := []struct {
		name     string
		n        int
		wantInit int
	}{
		{"negative clamped to 0", -5, 0},
		{"above limit clamped to 150", 200, 150},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := NewCounter(tt.n)
			if ct.remaining != tt.wantInit {
				t.Fatalf("NewCounter(%d).remaining = %d, want %d", tt.n, ct.remaining, tt.wantInit)
			}
		})
	}

	t.Run("concurrent decrements never exceed 150 successes", func(t *testing.T) {
		cc := NewCounter(150)
		var wg sync.WaitGroup
		var mu sync.Mutex
		successes := 0
		for i := 0; i < 300; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := cc.DecrementAudit()
				if err == nil {
					mu.Lock()
					successes++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if successes != 150 {
			t.Fatalf("expected exactly 150 successes, got %d", successes)
		}
	})
}

// TestDailyLimitExceededFailsClosed verifies that after exhaustion the next call fails closed.
func TestDailyLimitExceededFailsClosed(t *testing.T) {
	c := NewCounter(150)
	for i := 0; i < 150; i++ {
		_, err := c.DecrementAudit()
		if err != nil {
			t.Fatalf("unexpected error on call %d: %v", i+1, err)
		}
	}
	got, err := c.DecrementAudit()
	if !errors.Is(err, ErrD2011) {
		t.Fatalf("expected ErrD2011, got %v", err)
	}
	if got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

// TestZeroRemainingRejectsAudit verifies that zero-value counters and nil receivers are rejected.
func TestZeroRemainingRejectsAudit(t *testing.T) {
	c := NewCounter(0)
	got, err := c.DecrementAudit()
	if !errors.Is(err, ErrD2011) {
		t.Fatalf("expected ErrD2011 for NewCounter(0), got %v", err)
	}
	if got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}

	var zero Counter
	got, err = zero.DecrementAudit()
	if !errors.Is(err, ErrD2011) {
		t.Fatalf("expected ErrD2011 for zero Counter, got %v", err)
	}
	if got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}

	var nilC *Counter
	got, err = nilC.DecrementAudit()
	if !errors.Is(err, ErrD2011) {
		t.Fatalf("expected ErrD2011 for nil receiver, got %v", err)
	}
	if got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}
