package jobs

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

var revdecIDPattern = regexp.MustCompile(`^revdec:[0-9a-f]{64}$`)

func newReviewerStore(t *testing.T) (*Store, func() error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	db := openTestDB(t)
	if err := ApplyReviewerSchema(context.Background(), db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyReviewerSchema: %v", err)
	}
	return NewStore(db), db.Close
}

func sampleDecision(jobID string, at time.Time) ReviewerDecision {
	return ReviewerDecision{
		JobID: jobID, Phase: ReviewPhaseCompletion, RiskClass: string(RiskClassNormal),
		Outcome: ReviewOutcomeAssigned, Authors: []FamilyRef{{Family: "anthropic", Resolved: true}},
		Reviewer: FamilyRef{Family: "openai", Resolved: true}, FamilyDistinct: true, DecidedAt: at,
	}
}

func countReviewerRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + tableReviewerDecision).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// chainHas reports whether target itself (pointer identity) is in err's chain.
func chainHas(err error, target error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == target {
			return true
		}
	}
	return false
}

func TestReviewerDecisionRoundTrip(t *testing.T) {
	s, _ := newReviewerStore(t)
	ctx := context.Background()
	t1 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	first := sampleDecision("J", t1)
	second := sampleDecision("J", t2)
	second.Outcome = ReviewOutcomeHeld
	r1, err := s.RecordReviewerDecision(ctx, first)
	if err != nil {
		t.Fatalf("record first: %v", err)
	}
	r2, err := s.RecordReviewerDecision(ctx, second)
	if err != nil {
		t.Fatalf("record second: %v", err)
	}
	for _, r := range []ReviewerDecision{r1, r2} {
		if !revdecIDPattern.MatchString(r.ID) {
			t.Fatalf("ID %q does not match %s", r.ID, revdecIDPattern)
		}
	}
	if r1.ID == r2.ID {
		t.Fatalf("distinct decisions share ID %q", r1.ID)
	}
	again, err := s.RecordReviewerDecision(ctx, first)
	if err != nil || again.ID != r1.ID {
		t.Fatalf("repeat = %q, %v; want %q", again.ID, err, r1.ID)
	}
	got, err := s.ReviewerDecisions(ctx, "J")
	if err != nil {
		t.Fatalf("ReviewerDecisions: %v", err)
	}
	if want := []ReviewerDecision{r1, r2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReviewerDecisions(J) =\n%+v\nwant\n%+v", got, want)
	}
	if !got[0].DecidedAt.Equal(t1) || got[1].Outcome != ReviewOutcomeHeld || !got[0].FamilyDistinct {
		t.Fatalf("fields not preserved: %+v", got)
	}
	other, err := s.ReviewerDecisions(ctx, "other")
	if err != nil || other == nil || len(other) != 0 {
		t.Fatalf("other job = %v (nil=%v), %v; want empty slice", other, other == nil, err)
	}
}

func TestRecordReviewerDecisionRefusesInvalid(t *testing.T) {
	s, _ := newReviewerStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	cases := map[string]func(*ReviewerDecision){
		"phase":      func(d *ReviewerDecision) { d.Phase = "x" },
		"outcome":    func(d *ReviewerDecision) { d.Outcome = "ok" },
		"risk":       func(d *ReviewerDecision) { d.RiskClass = "bogus" },
		"job":        func(d *ReviewerDecision) { d.JobID = "" },
		"decided_at": func(d *ReviewerDecision) { d.DecidedAt = time.Time{} },
	}
	for name, mutate := range cases {
		d := sampleDecision("J", at)
		mutate(&d)
		_, err := s.RecordReviewerDecision(ctx, d)
		if !chainHas(err, ErrInvalidReviewerDecision) {
			t.Fatalf("%s: err = %v, want ErrInvalidReviewerDecision in chain", name, err)
		}
		if !errors.Is(err, ErrInvalidReviewerDecision) || !strings.Contains(err.Error(), "jobs: invalid reviewer decision") {
			t.Fatalf("%s: err = %v, want the sentinel message", name, err)
		}
	}
	if n := countReviewerRows(t, s); n != 0 {
		t.Fatalf("rows after invalid records = %d, want 0", n)
	}
	if _, err := s.RecordReviewerDecision(ctx, sampleDecision("J", at)); err != nil {
		t.Fatalf("valid record: %v", err)
	}
	if n := countReviewerRows(t, s); n != 1 {
		t.Fatalf("rows after valid record = %d, want 1", n)
	}
}

func TestReviewerDecisionClosedDB(t *testing.T) {
	s, closeDB := newReviewerStore(t)
	ctx := context.Background()
	if err := closeDB(); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, err := s.RecordReviewerDecision(ctx, sampleDecision("J", time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("record on closed db = %v, want KindUnavailable", err)
	}
	got, err := s.ReviewerDecisions(ctx, "J")
	if !cascade.HasKind(err, cascade.KindUnavailable) || got != nil {
		t.Fatalf("list on closed db = %v, %v; want nil, KindUnavailable", got, err)
	}
}

func TestReviewEvidenceArtifactRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l, store, _ := newLedgerFixture(t, now)
	ctx := context.Background()
	if err := ApplyReviewerSchema(ctx, store.db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyReviewerSchema: %v", err)
	}
	d, err := store.RecordReviewerDecision(ctx, sampleDecision("job-1", now))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	ref := ReviewEvidenceRef(d)
	if ref != "reviewer-decision:"+d.ID || !strings.HasPrefix(d.ID, "revdec:") {
		t.Fatalf("ReviewEvidenceRef = %q, want reviewer-decision:%s", ref, d.ID)
	}
	rec := passRecord(EvidenceReview, "idem-review")
	rec.ArtifactRef = ref
	if _, err := l.Append(ctx, rec, baseAuth()); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := l.Query(ctx, "job-1", EvidenceReview)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got.ArtifactRef != ref {
		t.Fatalf("ArtifactRef = %q, want %q", got.ArtifactRef, ref)
	}
}
