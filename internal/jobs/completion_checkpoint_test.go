package jobs

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestCheckpointMismatchCoversQA binds independent QA to the accepted checkpoint.
func TestCheckpointMismatchCoversQA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, checkpoint := range []string{"cp-other", "cp-1"} {
		t.Run(checkpoint, func(t *testing.T) {
			cp, store, ledger, bus, job := newCompletionFixture(t, RiskClassNormal)
			passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
			setJobState(t, store, &job, JobStateReviewing)
			rec := passRecord(EvidenceQA, "qa-checkpoint")
			rec.CheckpointID = checkpoint
			if _, err := ledger.Append(context.Background(), rec, baseAuth()); err != nil {
				t.Fatalf("Append QA: %v", err)
			}
			err := cp.Transition(context.Background(), checkpointRequest(&job))
			wantState, wantEvents := JobStateAccepted, 0
			if checkpoint == "cp-other" {
				denial, ok := err.(*ErrorCompletionDenied)
				if !ok || denial.CheckpointMismatch == nil || *denial.CheckpointMismatch != checkpoint {
					t.Fatalf("Transition = %v, want checkpoint mismatch %q", err, checkpoint)
				}
				wantState, wantEvents = JobStateReviewing, 1
			} else if err != nil {
				t.Fatalf("Transition with matching QA: %v", err)
			}
			if got := storedJobState(t, store, job.ID); got != wantState || job.State != wantState {
				t.Fatalf("stored/caller state = %s/%s, want %s", got, job.State, wantState)
			}
			if got := countGateEvents(t, bus); got != wantEvents {
				t.Fatalf("gate events = %d, want %d", got, wantEvents)
			}
		})
	}
}

// checkpointRequest evaluates acceptance against the fixture's checkpoint.
func checkpointRequest(job *Job) TransitionRequest {
	return TransitionRequest{
		Job: job, Target: JobStateAccepted, CheckpointID: "cp-1",
		Caller: PolicyEngineIdentity{EngineID: testEngineID}, PlannedRiskClass: RiskClassNormal,
	}
}

// TestCheckpointMismatchQueryErrorDenies distinguishes missing rows from unreadable bindings.
func TestCheckpointMismatchQueryErrorDenies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		name string
		kind EvidenceKind
		err  error
	}{
		{"unavailable", EvidenceReview, cascade.New(cascade.KindUnavailable, "boom")},
		{"other not found", EvidenceReview, cascade.New(cascade.KindNotFound, "other")},
		{"no evidence sentinel", EvidenceTests, ErrNoEvidence},
		{"real query", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp, store, ledger, bus, job := newCompletionFixture(t, RiskClassNormal)
			passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
			setJobState(t, store, &job, JobStateReviewing)
			if tc.err != nil {
				cp.queryEvidence = func(ctx context.Context, jobID string, kind EvidenceKind) (EvidenceRecord, error) {
					if kind == tc.kind {
						return EvidenceRecord{}, tc.err
					}
					return ledger.Query(ctx, jobID, kind)
				}
			}
			err := cp.Transition(context.Background(), checkpointRequest(&job))
			wantState, wantEvents := JobStateAccepted, 0
			if tc.err != nil && tc.err != ErrNoEvidence {
				denial, ok := err.(*ErrorCompletionDenied)
				if !ok || denial.Reason != "checkpoint binding unreadable: "+tc.err.Error() || denial.CheckpointMismatch != nil {
					t.Fatalf("Transition = %#v, want unreadable binding denial for %v", err, tc.err)
				}
				wantState, wantEvents = JobStateReviewing, 1
			} else if err != nil {
				t.Fatalf("Transition: %v", err)
			}
			if got := storedJobState(t, store, job.ID); got != wantState || job.State != wantState {
				t.Fatalf("stored/caller state = %s/%s, want %s", got, job.State, wantState)
			}
			if got := countGateEvents(t, bus); got != wantEvents {
				t.Fatalf("gate events = %d, want %d", got, wantEvents)
			}
		})
	}
}
