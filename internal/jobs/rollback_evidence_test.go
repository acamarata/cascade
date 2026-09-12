package jobs

import (
	"errors"
	"testing"
	"time"
)

func verifyingCandidate(gateReachedAt time.Time) ReleaseCandidate {
	return ReleaseCandidate{
		JobID:          "job-1",
		CommitSHA:      "sha-aaa",
		TreeHash:       "tree-aaa",
		GateSetVersion: "gateset-v1",
		GateReachedAt:  gateReachedAt,
	}
}

func passingRollback(producedAt time.Time) RollbackEvidence {
	return RollbackEvidence{
		PreviousReleaseRef: "v1.2.3",
		RollbackCommand:    "cascade release rollback v1.2.3",
		VerificationResult: OutcomePass,
		ProducedByJobID:    "build-job-0",
		ProducedAt:         producedAt,
	}
}

// TestVerifyRollbackEvidence_MissingFields asserts each of the five
// required fields, taken in turn, refuses with ErrMissingRollbackEvidence.
func TestVerifyRollbackEvidence_MissingFields(t *testing.T) {
	gateReachedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	producedAt := gateReachedAt.Add(-time.Hour)
	cand := verifyingCandidate(gateReachedAt)

	cases := map[string]func(*RollbackEvidence){
		"previous_release_ref": func(rb *RollbackEvidence) { rb.PreviousReleaseRef = "" },
		"rollback_command":     func(rb *RollbackEvidence) { rb.RollbackCommand = "" },
		"verification_result":  func(rb *RollbackEvidence) { rb.VerificationResult = "" },
		"produced_by_job_id":   func(rb *RollbackEvidence) { rb.ProducedByJobID = "" },
		"produced_at":          func(rb *RollbackEvidence) { rb.ProducedAt = time.Time{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			rb := passingRollback(producedAt)
			mutate(&rb)
			err := VerifyRollbackEvidence(rb, cand)
			if !errors.Is(err, ErrMissingRollbackEvidence) {
				t.Fatalf("missing %s: got %v, want ErrMissingRollbackEvidence", name, err)
			}
		})
	}
}

// TestVerifyRollbackEvidence_FailedVerification asserts a non-Pass
// VerificationResult is refused, never treated as "unset".
func TestVerifyRollbackEvidence_FailedVerification(t *testing.T) {
	gateReachedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	producedAt := gateReachedAt.Add(-time.Hour)
	cand := verifyingCandidate(gateReachedAt)

	for _, outcome := range []Outcome{OutcomeFail, OutcomePending} {
		t.Run(string(outcome), func(t *testing.T) {
			rb := passingRollback(producedAt)
			rb.VerificationResult = outcome
			if err := VerifyRollbackEvidence(rb, cand); !errors.Is(err, ErrMissingRollbackEvidence) {
				t.Fatalf("VerificationResult=%q: got %v, want ErrMissingRollbackEvidence", outcome, err)
			}
		})
	}
}

// TestVerifyRollbackEvidence_ProducedAfterGate asserts evidence produced
// AT OR AFTER the gate node was reached is refused -- a rollback
// asserted after the fact is not evidence.
func TestVerifyRollbackEvidence_ProducedAfterGate(t *testing.T) {
	gateReachedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cand := verifyingCandidate(gateReachedAt)

	for name, producedAt := range map[string]time.Time{
		"same_instant": gateReachedAt,
		"after":        gateReachedAt.Add(time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			rb := passingRollback(producedAt)
			if err := VerifyRollbackEvidence(rb, cand); !errors.Is(err, ErrMissingRollbackEvidence) {
				t.Fatalf("produced %s the gate: got %v, want ErrMissingRollbackEvidence", name, err)
			}
		})
	}
}

// TestVerifyRollbackEvidence_Valid asserts a complete, passing,
// before-the-gate value verifies with no error.
func TestVerifyRollbackEvidence_Valid(t *testing.T) {
	gateReachedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rb := passingRollback(gateReachedAt.Add(-time.Hour))
	cand := verifyingCandidate(gateReachedAt)
	if err := VerifyRollbackEvidence(rb, cand); err != nil {
		t.Fatalf("valid rollback evidence: unexpected error %v", err)
	}
}

// TestRollbackEvidenceArtifactRef_ContentAddressed asserts the ref is
// deterministic for identical evidence, changes when the evidence
// changes, and is never empty -- the "names a real artifact rather than
// a convention" acceptance criterion.
func TestRollbackEvidenceArtifactRef_ContentAddressed(t *testing.T) {
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rb1 := passingRollback(base.Add(-time.Hour))
	rb2 := passingRollback(base.Add(-time.Hour))
	if ref1, ref2 := rollbackEvidenceArtifactRef(rb1), rollbackEvidenceArtifactRef(rb2); ref1 != ref2 {
		t.Fatalf("identical evidence produced different refs: %q vs %q", ref1, ref2)
	}
	if rollbackEvidenceArtifactRef(rb1) == "" {
		t.Fatal("artifact ref must never be empty")
	}

	rb3 := passingRollback(base.Add(-time.Hour))
	rb3.PreviousReleaseRef = "v9.9.9"
	if rollbackEvidenceArtifactRef(rb1) == rollbackEvidenceArtifactRef(rb3) {
		t.Fatal("different evidence produced the same ref")
	}
}
