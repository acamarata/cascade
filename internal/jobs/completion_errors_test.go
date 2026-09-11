package jobs

// Purpose: the completion gate's sentinel and denial-message contract:
//
//	every sentinel is distinct and non-nil, and ErrorCompletionDenied's
//	Error() names every populated field.
//
// SPORT: jobs/completion-gate/ADD (P1-E29-W6-S60-T3).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestCompletionSentinelsDistinctAndNonNil proves the six sentinels are
// distinct ALLOCATIONS. Pointer identity (==), never errors.Is: pkg/
// cascade.Error.Is compares by Kind alone (errors.go), so two sentinels
// sharing a Kind (e.g. both ErrUnknownEvidenceKind and
// ErrUnknownProducerCapability are KindInvalidInput) are errors.Is-equal
// to each other by that shared-Kind rule while still being the distinct
// values this test needs to tell apart -- exactly the "a check that
// shares the bug it checks for" trap the AGENT-BRIEF warns about, caught
// here by using == instead.
func TestCompletionSentinelsDistinctAndNonNil(t *testing.T) {
	sentinels := []error{
		ErrNoEvidence, ErrUnknownEvidenceKind, ErrUnknownProducerCapability,
		ErrEvidenceProducerDenied, ErrNotPolicyEngine, ErrEvidenceChainBroken,
	}
	for i, a := range sentinels {
		if a == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if a == b {
				t.Errorf("sentinel %d (%v) == sentinel %d (%v), want distinct allocations", i, a, j, b)
			}
		}
	}
}

func TestErrorCompletionDeniedExplainWhy(t *testing.T) {
	wrong := JobStateRunning
	denial := &ErrorCompletionDenied{
		MissingEvidence: []EvidenceKind{EvidenceBuild, EvidenceLint},
		WrongSuccessor:  &wrong,
		ExpiredApproval: true,
		Reason:          "token consumed",
	}
	msg := denial.Error()
	for _, want := range []string{"build", "lint", "running", "token consumed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to name %q", msg, want)
		}
	}
}

func TestErrorCompletionDeniedNeverEmpty(t *testing.T) {
	denial := &ErrorCompletionDenied{}
	if denial.Error() == "" {
		t.Fatal("Error() on a zero-value denial is empty, want a non-empty fallback")
	}
}

func TestApprovalScopeActionDeterministicAndDistinct(t *testing.T) {
	a := ApprovalScopeAction("job-1", "tree-1", "pv-1")
	b := ApprovalScopeAction("job-1", "tree-1", "pv-1")
	if a != b {
		t.Fatalf("ApprovalScopeAction is not deterministic: %q != %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("ApprovalScopeAction len = %d, want 64 (hex-SHA256, within the queue's 64-char ceiling)", len(a))
	}
	if c := ApprovalScopeAction("job-2", "tree-1", "pv-1"); c == a {
		t.Fatal("ApprovalScopeAction for a different job_id collided")
	}
}

func TestCompletionOutOfLeaseScopePushesAttention(t *testing.T) {
	cp, _, ledger, _, job := newCompletionFixture(t, RiskClassLow)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	clock := runtime.NewFixedClock(time.Now())
	kv := storetest.NewMemStore()
	n := 0
	attention := supervision.NewStore(kv, clock, nil, func() string { n++; return "attn-id" }, 0)
	cp.deps.Attention = attention

	err := cp.Transition(context.Background(), TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassLow,
		ActualFootprint:  ChangeFootprint{ChangedPaths: []string{"README.md"}},
	})
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || len(denial.OutOfLeaseScope) == 0 {
		t.Fatalf("Transition with no lease scope prefixes = %v, want OutOfLeaseScope", err)
	}
	if n == 0 {
		t.Fatal("pushOutOfScopeAttention did not push through the real supervision.Store")
	}
}

func TestCompletionOutOfLeaseScopeWithNoAttentionConfigured(t *testing.T) {
	cp, _, ledger, _, job := newCompletionFixture(t, RiskClassLow)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	err := cp.Transition(context.Background(), TransitionRequest{
		Job: &job, Target: JobStateVerifying, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassLow,
		ActualFootprint:  ChangeFootprint{ChangedPaths: []string{"README.md"}},
	})
	if _, ok := err.(*ErrorCompletionDenied); !ok {
		t.Fatalf("Transition with no attention sink configured = %v, want *ErrorCompletionDenied", err)
	}
}

func TestCompletionCheckApprovalWithNoQueueConfigured(t *testing.T) {
	cp, _, ledger, _, job := newCompletionFixture(t, RiskClassCritical)
	job.State = JobStateReviewing
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview, EvidenceAdversarial})
	err := cp.Transition(context.Background(), TransitionRequest{
		Job: &job, Target: JobStateAccepted, Caller: PolicyEngineIdentity{EngineID: testEngineID},
		PlannedRiskClass: RiskClassCritical,
	})
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || !denial.ExpiredApproval {
		t.Fatalf("Transition to accepted with no approval queue configured = %v, want ExpiredApproval", err)
	}
}

func TestCompletionCommitDeniesOnCursorRace(t *testing.T) {
	cp, _, ledger, _, job := newCompletionFixture(t, RiskClassNormal)
	passAll(t, ledger, []EvidenceKind{EvidenceBuild, EvidenceLint, EvidenceTests, EvidenceReview})
	ctx := context.Background()
	staleCursor, err := ledger.Cursor(ctx, job.ID)
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	// A row lands AFTER the evaluation read but BEFORE commit -- the
	// exact race commit's re-read inside its own transaction must catch.
	if _, err := ledger.Append(ctx, passRecord(EvidenceAdversarial, "idem-race"), AppendAuthorization{ExecutionID: "exec-1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	err = cp.commit(ctx, TransitionRequest{Job: &job, Target: JobStateVerifying, SessionID: "s", TicketID: "t"}, staleCursor)
	denial, ok := err.(*ErrorCompletionDenied)
	if !ok || denial.Reason == "" {
		t.Fatalf("commit at a stale cursor = %v, want a Reason-carrying denial", err)
	}
}

func TestEvidenceCursorOnClosedStore(t *testing.T) {
	l, store, _ := newLedgerFixture(t, time.Now())
	_ = store.db.Close()
	if _, err := l.Cursor(context.Background(), "job-1"); err == nil {
		t.Error("Cursor on a closed store = nil error, want a refusal")
	}
	if _, err := l.Append(context.Background(), passRecord(EvidenceBuild, "idem-closed"), baseAuth()); err == nil {
		t.Error("Append on a closed store = nil error, want a refusal")
	}
}

func TestApplyEvidenceSchemaRequiresCollaborators(t *testing.T) {
	if err := ApplyEvidenceSchema(context.Background(), nil, nil, nil, "", ""); err == nil {
		t.Error("ApplyEvidenceSchema with a nil db = nil error, want a refusal")
	}
	path := t.TempDir() + "/schema-nilclock.db"
	db, err := openEvidenceDB(t, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := ApplyEvidenceSchema(context.Background(), db, nil, nil, "", ""); err == nil {
		t.Error("ApplyEvidenceSchema with a nil clock = nil error, want a refusal")
	}
}

func TestNewCompletionPolicyRequiresCollaborators(t *testing.T) {
	full := CompletionPolicyDeps{Store: &Store{}, Ledger: &EvidenceLedger{}, Clock: fakeClock{}, EngineID: "e1"}
	cases := map[string]func(d *CompletionPolicyDeps){
		"store":  func(d *CompletionPolicyDeps) { d.Store = nil },
		"ledger": func(d *CompletionPolicyDeps) { d.Ledger = nil },
		"clock":  func(d *CompletionPolicyDeps) { d.Clock = nil },
		"engine": func(d *CompletionPolicyDeps) { d.EngineID = "" },
	}
	for name, mutate := range cases {
		d := full
		mutate(&d)
		if _, err := NewCompletionPolicy(d); err == nil {
			t.Errorf("NewCompletionPolicy with no %s = nil error, want a refusal", name)
		}
	}
}
