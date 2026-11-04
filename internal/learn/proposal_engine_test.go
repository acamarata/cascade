package learn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

type patternLookup struct {
	found  bool
	err    error
	seen   []string
	status Status
}

func (l *patternLookup) LatestBySignature(_ context.Context, sig string) (Proposal, bool, error) {
	l.seen = append(l.seen, sig)
	return Proposal{Config: LearnedConfig{Status: l.status}}, l.found, l.err
}

func seedPatternOutcome(t *testing.T, db *sql.DB, at time.Time, n int, lane, component string) {
	t.Helper()
	o := baseOutcome(fmt.Sprintf("pattern-job-%d", n))
	o.Component, o.NodeID = component, lane
	if err := NewSQLiteOutcomeWriter(db, fakeClock{t: at}).Record(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	f := Finding{JobID: o.JobID, Family: FamilyToolFailure, Category: CategoryCrash, Severity: SeverityNormal, Count: 100}
	if err := NewSQLiteFindingWriter(db).WriteFinding(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	// A second finding row must not become a second occurrence.
	if err := NewSQLiteFindingWriter(db).WriteFinding(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

// TestDetectPatternsThresholds exercises real telemetry joins and fixed support gates.
func TestDetectPatternsThresholds(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		lanes     []string
		component string
		age       time.Duration
		want      int
	}{
		{"two-supporters", []string{"beneficiary", "a", "b"}, "generated-code", time.Hour, 0},
		{"three-supporters", []string{"beneficiary", "a", "b", "c"}, "generated-code", time.Hour, 1},
		{"same-source", []string{"beneficiary", "a", "a", "a"}, "generated-code", time.Hour, 0},
		{"beneficiary-excluded", []string{"beneficiary", "beneficiary", "beneficiary"}, "generated-code", time.Hour, 0},
		{"boundary", []string{"beneficiary", "a", "b", "c"}, "generated-code", Window, 1},
		{"expired", []string{"beneficiary", "a", "b", "c"}, "generated-code", Window + time.Second, 0},
		{"future", []string{"beneficiary", "a", "b", "c"}, "generated-code", -time.Second, 0},
		{"no-rule", []string{"beneficiary", "a", "b", "c"}, "unknown", time.Hour, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestOutcomeDB(t)
			for i, lane := range tc.lanes {
				seedPatternOutcome(t, db, now.Add(-tc.age), i, lane, tc.component)
			}
			lookup := &patternLookup{}
			subs, triggers, err := DetectPatterns(context.Background(), NewSQLiteOutcomeReader(db), lookup, now, Window)
			if err != nil {
				t.Fatal(err)
			}
			if len(subs) != tc.want || len(triggers) != tc.want {
				t.Fatalf("got %d submissions, %d triggers; want %d", len(subs), len(triggers), tc.want)
			}
			if tc.want == 0 {
				return
			}
			checkDetectedTrigger(t, db, now, subs, triggers[0], lookup)
		})
	}
}

// checkDetectedTrigger checks one detected trigger's support and window, its
// signature lookup, and that its submission is accepted by the config store.
func checkDetectedTrigger(t *testing.T, db *sql.DB, now time.Time, subs []Submission, tr Trigger, lookup *patternLookup) {
	t.Helper()
	if tr.Occurrences != 3 || tr.DistinctSources != 3 || tr.Kind != "ci" {
		t.Fatalf("wrong trigger: %+v", tr)
	}
	if !tr.WindowStart.Equal(now.Add(-Window)) || !tr.WindowEnd.Equal(now) {
		t.Fatal("wrong window")
	}
	if tr.Beneficiary.NodeID != "beneficiary" || len(subs[0].Evidence) != 3 {
		t.Fatal("beneficiary counted as support")
	}
	if len(lookup.seen) != 1 || lookup.seen[0] != tr.Signature {
		t.Fatal("signature not looked up")
	}
	if err := applyConfigSchema(db); err != nil {
		t.Fatal(err)
	}
	store, err := NewConfigStore(db, fakeClock{t: now})
	if err != nil {
		t.Fatal(err)
	}
	config, err := store.Submit(context.Background(), subs[0])
	if err != nil {
		t.Fatal(err)
	}
	cls, err := ClassifyChange(subs[0].Target, subs[0].Change)
	if err != nil || config.Tier != cls.Tier || config.Status != StatusPending {
		t.Fatalf("authority mismatch: %+v, %v", config, err)
	}
}

// TestDetectPatternsSkipsExistingProposal covers signature idempotency and lookup failures.
func TestDetectPatternsSkipsExistingProposal(t *testing.T) {
	now := newTestClock().Now()
	db := newTestOutcomeDB(t)
	for i, lane := range []string{"beneficiary", "a", "b", "c"} {
		seedPatternOutcome(t, db, now.Add(-time.Hour), i, lane, "generated-code")
	}
	for _, status := range []Status{StatusPending, StatusApplied, StatusRejected, StatusReverted} {
		l := &patternLookup{found: true, status: status}
		subs, triggers, err := DetectPatterns(context.Background(), NewSQLiteOutcomeReader(db), l, now, Window)
		if err != nil || len(subs) != 0 || len(triggers) != 0 || len(l.seen) != 1 {
			t.Fatalf("existing proposal %s: %v, %v, %v", status, subs, triggers, err)
		}
	}
	l := &patternLookup{err: errors.New("lookup failed")}
	subs, triggers, err := DetectPatterns(context.Background(), NewSQLiteOutcomeReader(db), l, now, Window)
	if !cascade.HasKind(err, cascade.KindUnavailable) || subs != nil || triggers != nil {
		t.Fatalf("lookup error did not refuse: %v", err)
	}
}

// TestDetectPatternsAcrossRepos checks shared support with repo-specific signatures.
func TestDetectPatternsAcrossRepos(t *testing.T) {
	db := newTestOutcomeDB(t)
	now := newTestClock().Now()
	for i, lane := range []string{"beneficiary", "a", "b", "c"} {
		seedPatternOutcome(t, db, now.Add(-time.Hour), i, lane, "generated-code")
		if _, err := db.Exec(`UPDATE jobs_telemetry_outcomes SET repo_id = ? WHERE job_id = ?`,
			fmt.Sprintf("repo-%d", i), fmt.Sprintf("pattern-job-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	subs, triggers, err := DetectPatterns(context.Background(), NewSQLiteOutcomeReader(db), &patternLookup{}, now, Window)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 4 || len(triggers) != 4 {
		t.Fatalf("got %d repo proposals, want 4", len(subs))
	}
	seen := map[string]bool{}
	for _, tr := range triggers {
		if seen[tr.Signature] || tr.Occurrences != 3 || tr.DistinctSources != 3 {
			t.Fatalf("wrong repo trigger: %+v", tr)
		}
		seen[tr.Signature] = true
	}
}

type failedOutcomeReader struct{ err error }

func (r failedOutcomeReader) ReadWindow(context.Context, time.Time, time.Time) ([]OutcomeRow, error) {
	return nil, r.err
}

// TestDetectPatternsReadErrorRefuses verifies typed refusal and preserves the cause.
func TestDetectPatternsReadErrorRefuses(t *testing.T) {
	cause := errors.New("read failed")
	l := &patternLookup{}
	subs, triggers, err := DetectPatterns(context.Background(), failedOutcomeReader{cause}, l, newTestClock().Now(), Window)
	if !cascade.HasKind(err, cascade.KindUnavailable) || !errors.Is(err, cause) {
		t.Fatalf("wrong read refusal: %v", err)
	}
	if subs != nil || triggers != nil || len(l.seen) != 0 {
		t.Fatal("read error produced partial results")
	}
}
