package nodes

// Purpose (this file): journal continuity across a re-queue — that a
//   replacement attempt starts from what the lost one recorded, and that
//   "I could not read the journal" never reads as "there is nothing in it".
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T3.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRequeueJournalContinuity is the acceptance the ticket names: the
// resumed run continues from the checkpoint, not from scratch.
func TestRequeueJournalContinuity(t *testing.T) {
	reader := fixedContinuity{records: []StreamedRecord{
		{Seq: 7, Attempt: 1, OperationID: "op-1"},
		{Seq: 8, Attempt: 1, OperationID: "op-2"},
		{Seq: 9, Attempt: 1, OperationID: "op-2"}, // the same operation, recorded twice
	}}

	point, err := ResumeFrom(context.Background(), reader, "job-7")
	if err != nil {
		t.Fatalf("ResumeFrom: %v", err)
	}
	if point.FromScratch {
		t.Fatal("an entity with records resumed from scratch, discarding the lost attempt's work")
	}
	if point.Seq != 9 {
		t.Errorf("resume sequence = %d, want the highest recorded (9)", point.Seq)
	}
	if got := len(point.CompletedOperations); got != 2 {
		t.Fatalf("%d completed operation(s), want 2 distinct: %v", got, point.CompletedOperations)
	}
	if !point.Completed("op-1") || !point.Completed("op-2") {
		t.Errorf("completed set %v does not cover both recorded operations", point.CompletedOperations)
	}
	if point.Completed("op-3") {
		t.Error("an operation that was never recorded is reported completed")
	}
}

// TestAnEmptyJournalIsAColdStartNotAnError separates the two states a
// caller must act on differently.
func TestAnEmptyJournalIsAColdStartNotAnError(t *testing.T) {
	point, err := ResumeFrom(context.Background(), fixedContinuity{}, "job-7")
	if err != nil {
		t.Fatalf("an entity with no records reported an error: %v", err)
	}
	if !point.FromScratch {
		t.Error("an entity with no records did not report a cold start")
	}
	if point.Seq != 0 || len(point.CompletedOperations) != 0 {
		t.Errorf("a cold start carries history: %+v", point)
	}
}

// TestAnUnwiredJournalReaderIsNotAColdStart is the distinction that makes
// this type safe. Both produce the same ResumePoint and mean opposite
// things: one says nothing was lost, the other says we cannot tell — and
// acting on the first when the second is true re-runs completed work.
func TestAnUnwiredJournalReaderIsNotAColdStart(t *testing.T) {
	_, err := ResumeFrom(context.Background(), nil, "job-7")
	if err == nil {
		t.Fatal("a controller with no journal reader resumed from scratch")
	}
	if !strings.Contains(err.Error(), "job-7") {
		t.Errorf("error = %v, want it to name the entity that could not be read", err)
	}
}

// TestAJournalReadFailureIsReported covers the other unreadable case: the
// reader is wired and the read fails.
func TestAJournalReadFailureIsReported(t *testing.T) {
	boom := errors.New("the journal store is unavailable")
	if _, err := ResumeFrom(context.Background(),
		fixedContinuity{err: boom}, "job-7"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the reader's own error", err)
	}
}

// TestResumingWithNoEntityIsRefused covers the boundary: a resume point for
// no entity would silently be a cold start for everything.
func TestResumingWithNoEntityIsRefused(t *testing.T) {
	if _, err := ResumeFrom(context.Background(), fixedContinuity{}, ""); err == nil {
		t.Fatal("a resume point was built for no entity")
	}
}

// TestAPlannedRequeueCarriesTheResumePoint proves the two halves are joined
// — a plan that placed the work somewhere but forgot where it had got to
// would re-run the lost attempt from the beginning on a healthy node, which
// is the failure this ticket is about.
func TestAPlannedRequeueCarriesTheResumePoint(t *testing.T) {
	deps, _ := requeueHarness(t)
	deps.Attempts.Next("d1")

	plan, err := PlanRequeue(context.Background(), deps, idempotentWork(), LossTunnel)
	if err != nil {
		t.Fatalf("PlanRequeue: %v", err)
	}
	if plan.Resume.FromScratch {
		t.Fatal("the replacement was planned to start over despite recorded work")
	}
	if plan.Resume.EntityID != "job-7" || plan.Resume.Seq == 0 {
		t.Errorf("resume point = %+v, want the lost attempt's entity and position", plan.Resume)
	}
}

// TestARequeueCannotBePlannedWithoutContinuity proves the reader is
// required on the automatic path too, not only when a caller asks for a
// resume point directly.
func TestARequeueCannotBePlannedWithoutContinuity(t *testing.T) {
	deps, _ := requeueHarness(t)
	deps.Continuity = nil

	if _, err := PlanRequeue(context.Background(), deps, idempotentWork(), LossHeartbeat); err == nil {
		t.Fatal("a replacement was planned with no idea what the lost attempt had done")
	}
}
