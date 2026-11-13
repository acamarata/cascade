package jobs

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestCompletionHighRequiresQAEvidence checks missing, failing and passing QA rows.
func TestCompletionHighRequiresQAEvidence(t *testing.T) {
	isolateQAHome(t)
	cp, store, ledger, _, job := newCompletionFixture(t, RiskClassHigh)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial})
	req := TransitionRequest{
		Job: &job, Target: JobStateVerifying,
		Caller: PolicyEngineIdentity{EngineID: testEngineID}, PlannedRiskClass: RiskClassHigh,
	}
	t.Run("absent", func(t *testing.T) { assertHighQAMissing(t, cp, store, req, true) })
	for _, tc := range []struct {
		name    string
		outcome Outcome
		missing bool
	}{
		{"failing", OutcomeFail, true},
		{"passing", OutcomePass, false},
		{"latest failing", OutcomeFail, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := passRecord(EvidenceQA, "qa-"+tc.name)
			rec.Outcome = tc.outcome
			written, err := ledger.Append(context.Background(), rec, baseAuth())
			if err != nil {
				t.Fatalf("Append QA: %v", err)
			}
			if !strings.HasPrefix(written.AttestorIdentity, "daemon:") {
				t.Fatalf("QA identity = %q, want daemon: prefix", written.AttestorIdentity)
			}
			assertHighQAMissing(t, cp, store, req, tc.missing)
		})
	}
}

func assertHighQAMissing(t *testing.T, cp *CompletionPolicy, store *Store, req TransitionRequest, want bool) {
	t.Helper()
	err := cp.Transition(context.Background(), req)
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok {
		t.Fatalf("Transition = %v, want evidence denial", err)
	}
	if got := containsEvidence(denial.MissingEvidence, EvidenceQA); got != want {
		t.Fatalf("MissingEvidence = %v, want QA missing = %v", denial.MissingEvidence, want)
	}
	for _, kind := range []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial} {
		if containsEvidence(denial.MissingEvidence, kind) {
			t.Fatalf("passing %s evidence is missing", kind)
		}
	}
	stored, found, err := store.GetJob(context.Background(), req.Job.ID)
	if err != nil || !found || stored.State != JobStateRunning || req.Job.State != JobStateRunning {
		t.Fatalf("denial changed state: stored=%s request=%s found=%v err=%v", stored.State, req.Job.State, found, err)
	}
}

// TestEvidenceKindsForGateSetQA pins QA to High and Critical gate sets.
func TestEvidenceKindsForGateSetQA(t *testing.T) {
	for _, tc := range []struct {
		class RiskClass
		want  int
	}{
		{RiskClassLow, 0},
		{RiskClassNormal, 0},
		{RiskClassHigh, 1},
		{RiskClassCritical, 1},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			gates, err := GateSetForRiskClass(tc.class)
			if err != nil {
				t.Fatal(err)
			}
			// Repeated gates must still require only one QA row.
			gates = append(gates, gates...)
			count := 0
			for _, kind := range evidenceKindsForGateSet(gates) {
				if kind == EvidenceQA {
					count++
				}
			}
			if count != tc.want {
				t.Fatalf("QA count = %d, want %d", count, tc.want)
			}
		})
	}
}

// TestEvidenceQAProducerIdentity checks identity refusals before ledger writes.
func TestEvidenceQAProducerIdentity(t *testing.T) {
	l := newQALedgerFixture(t)
	ctx := context.Background()
	for _, identity := range []string{"node:n-1", "approval:a-1", ""} {
		rec := passRecord(EvidenceQA, "qa-denied-"+identity)
		rec.AttestorIdentity = identity
		_, err := l.Append(ctx, rec, baseAuth())
		cause := errors.Unwrap(err)
		if cause != ErrEvidenceProducerDenied || cause.Error() != "permission-denied: jobs: evidence producer denied" {
			t.Fatalf("Append(%q) = %v, want exact ErrEvidenceProducerDenied sentinel and message", identity, err)
		}
		if !strings.Contains(err.Error(), `evidence kind "qa" requires an attestor_identity prefixed "daemon:"`) {
			t.Fatalf("Append(%q) error = %q, want QA daemon identity explanation", identity, err)
		}
		rows, queryErr := l.QueryAll(ctx, "job-1")
		if queryErr != ErrNoEvidence || queryErr.Error() != "not-found: jobs: no evidence record for that kind" || len(rows) != 0 {
			t.Fatalf("rows after denied Append(%q) = %v, %v", identity, rows, queryErr)
		}
	}
	rec, err := l.Append(ctx, passRecord(EvidenceQA, "qa-accepted"), baseAuth())
	if err != nil || rec.Seq != 1 || rec.AttestorIdentity != "daemon:d-1" {
		t.Fatalf("daemon QA Append = %+v, %v", rec, err)
	}
	rows, err := l.QueryAll(ctx, "job-1")
	if err != nil || len(rows) != 1 || rows[0].Kind != EvidenceQA || rows[0].Seq != rec.Seq {
		t.Fatalf("rows after daemon QA Append = %v, %v", rows, err)
	}
}

func newQALedgerFixture(t *testing.T) *EvidenceLedger {
	t.Helper()
	isolateQAHome(t)
	l, _, _ := newLedgerFixture(t, time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC))
	return l
}

func isolateQAHome(t *testing.T) {
	t.Helper()
	operatorHome := os.Getenv("HOME")
	home := t.TempDir()
	if qaHomeIsolated(operatorHome, home) {
		t.Fatal("isolation guard accepted operator HOME")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
		if !qaHomeIsolated(os.Getenv(key), home) {
			t.Fatalf("isolation guard refused temporary %s", key)
		}
	}
}

func qaHomeIsolated(home, temporaryHome string) bool {
	return home != "" && home == temporaryHome
}
