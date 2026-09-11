package jobs

import (
	"context"
	"testing"
)

func TestPutAndGetJob(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-1")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	got, ok, err := s.GetJob(ctx, "job-1")
	if err != nil || !ok {
		t.Fatalf("GetJob: got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.ID != j.ID || got.State != j.State || got.MutableScope != j.MutableScope {
		t.Errorf("GetJob = %+v, want %+v", got, j)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "code" {
		t.Errorf("GetJob capabilities = %v, want [code]", got.Capabilities)
	}
	if got.ConsecutiveFailedAttempts != 0 {
		t.Errorf("GetJob ConsecutiveFailedAttempts = %d, want 0 (application-layer default)", got.ConsecutiveFailedAttempts)
	}
}

func TestGetJobNotFoundIsNoErrorNoOK(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, ok, err := s.GetJob(ctx, "never-created")
	if err != nil {
		t.Fatalf("GetJob: err=%v, want nil", err)
	}
	if ok {
		t.Error("GetJob: ok=true, want false")
	}
}

func TestPutJobRejectsUnknownState(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-bad-state")
	j.State = JobState("bogus")
	if err := s.PutJob(ctx, j); err == nil {
		t.Error("PutJob(unknown state) = nil, want error")
	}
}

func TestPutJobRejectsUnknownConsequenceAndDataClass(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-bad-consequence")
	j.ConsequenceClass = ConsequenceClass("bogus")
	if err := s.PutJob(ctx, j); err == nil {
		t.Error("PutJob(unknown consequence_class) = nil, want error")
	}
	j2 := baseJob("job-bad-data")
	j2.DataClass = DataClass("bogus")
	if err := s.PutJob(ctx, j2); err == nil {
		t.Error("PutJob(unknown data_class) = nil, want error")
	}
}

// TestPutTransitionUnknownTransition covers the plan-named
// unknown-transition error path.
func TestPutTransitionUnknownTransition(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-2")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	// pending -> accepted is not a legal public edge.
	if err := s.PutTransition(ctx, j.ID, JobStateAccepted, 2); err == nil {
		t.Error("PutTransition(pending->accepted) = nil, want typed unknown-transition error")
	}
	got, _, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != JobStatePending {
		t.Errorf("job state after refused transition = %q, want unchanged pending", got.State)
	}
}

func TestPutTransitionPolicyReservedRefusedOnPublicPath(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-3")
	j.State = JobStateRunning
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := s.PutTransition(ctx, j.ID, JobStateVerifying, 2); err == nil {
		t.Error("PutTransition(running->verifying) on public path = nil, want refusal")
	}
}

func TestPutTransitionLegalEdgeSucceeds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-4")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if err := s.PutTransition(ctx, j.ID, JobStateLeased, 2); err != nil {
		t.Fatalf("PutTransition(pending->leased): %v", err)
	}
	got, _, err := s.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.State != JobStateLeased {
		t.Errorf("job state = %q, want leased", got.State)
	}
	if got.UpdatedAt != 2 {
		t.Errorf("job updated_at = %d, want 2", got.UpdatedAt)
	}
}

func TestPutTransitionMissingJob(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutTransition(ctx, "never-created", JobStateLeased, 1); err == nil {
		t.Error("PutTransition(missing job) = nil, want not-found error")
	}
}

func TestPutAndListDependencies(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	a, b := baseJob("job-a"), baseJob("job-b")
	if err := s.PutJob(ctx, a); err != nil {
		t.Fatalf("PutJob a: %v", err)
	}
	if err := s.PutJob(ctx, b); err != nil {
		t.Fatalf("PutJob b: %v", err)
	}
	if err := s.PutDependency(ctx, TaskDependency{JobID: a.ID, DependsOnJobID: b.ID}); err != nil {
		t.Fatalf("PutDependency: %v", err)
	}
	deps, err := s.Dependencies(ctx, a.ID)
	if err != nil {
		t.Fatalf("Dependencies: %v", err)
	}
	if len(deps) != 1 || deps[0] != b.ID {
		t.Errorf("Dependencies(a) = %v, want [%s]", deps, b.ID)
	}
}

func TestPutDependencyRejectsEmptyIDs(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.PutDependency(ctx, TaskDependency{}); err == nil {
		t.Error("PutDependency(empty ids) = nil, want error")
	}
}

func TestGetJobDecodeErrorOnBadStoredClasses(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	j := baseJob("job-raw-bad")
	if err := s.PutJob(ctx, j); err != nil {
		t.Fatalf("PutJob: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE `+tableJob+` SET consequence_class = 'bogus' WHERE id = ?`, j.ID); err != nil {
		t.Fatalf("corrupt consequence_class: %v", err)
	}
	if _, _, err := s.GetJob(ctx, j.ID); err == nil {
		t.Error("GetJob(corrupted consequence_class) = nil error, want typed decode error")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE `+tableJob+` SET consequence_class = ?, data_class = 'bogus' WHERE id = ?`,
		string(ConsequenceNormal), j.ID); err != nil {
		t.Fatalf("corrupt data_class: %v", err)
	}
	if _, _, err := s.GetJob(ctx, j.ID); err == nil {
		t.Error("GetJob(corrupted data_class) = nil error, want typed decode error")
	}
}
