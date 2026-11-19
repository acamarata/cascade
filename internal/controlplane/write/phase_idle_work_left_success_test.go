package write

import (
	"testing"
)

type successStore struct {
	calls int
	count int
	err   error
}

func (s *successStore) PendingTasks(phase string) (int, error) {
	s.calls++
	return s.count, s.err
}

// TestCheckPhaseIdleWorkLeftReturnsReport verifies that a valid phase with a positive
// pending task count returns the expected report, crunchCreated=false, and no error.
func TestCheckPhaseIdleWorkLeftReturnsReport(t *testing.T) {
	store := &successStore{count: 2}
	r, crunch, err := CheckPhaseIdleWorkLeft(store, "phase_idle_work_left")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if crunch {
		t.Fatalf("expected crunchCreated=false, got true")
	}
	if r == nil {
		t.Fatalf("expected non-nil report")
	}
	if r.Phase != "phase_idle_work_left" {
		t.Errorf("expected Phase='phase_idle_work_left', got %q", r.Phase)
	}
	if r.WorkLeft != 2 {
		t.Errorf("expected WorkLeft=2, got %d", r.WorkLeft)
	}
	if store.calls != 1 {
		t.Errorf("expected exactly 1 store read, got %d", store.calls)
	}
}

// TestCheckPhaseIdleWorkLeftZeroWork verifies that a zero pending task count is treated
// as a valid outcome, returning a non-nil report with WorkLeft=0.
func TestCheckPhaseIdleWorkLeftZeroWork(t *testing.T) {
	store := &successStore{count: 0}
	r, crunch, err := CheckPhaseIdleWorkLeft(store, "zero_phase")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if crunch {
		t.Fatalf("expected crunchCreated=false, got true")
	}
	if r == nil {
		t.Fatalf("expected non-nil report for zero work")
	}
	if r.WorkLeft != 0 {
		t.Errorf("expected WorkLeft=0, got %d", r.WorkLeft)
	}
}

// TestCheckPhaseIdleWorkLeftDoesNotCreateCrunch verifies that the read-only store
// remains unchanged and no crunch creation occurs on the success path.
func TestCheckPhaseIdleWorkLeftDoesNotCreateCrunch(t *testing.T) {
	store := &successStore{count: 2}
	_, crunch, err := CheckPhaseIdleWorkLeft(store, "phase_idle_work_left")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if crunch {
		t.Fatalf("expected crunchCreated=false, got true")
	}
	if store.calls != 1 {
		t.Errorf("expected store to remain read-only with exactly 1 call, got %d", store.calls)
	}
}
