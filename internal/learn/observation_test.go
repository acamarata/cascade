package learn

// Purpose: the fallback chain (repo -> lang -> global), asserted by stored
//   key form and by the level Posterior reports, and the observation mapping
//   an outcome takes into a scored cell.
// SPORT: internal.learn.Posterior/TESTED, internal.learn.observationFor/TESTED (P1-CAP-03).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordLangOutcome records one outcome so repoID has a known language.
func recordLangOutcome(t *testing.T, s *SQLiteCapabilityScorer, jobID, repoID string, lang Language) {
	t.Helper()
	o := baseOutcome(jobID)
	o.RepoID, o.Language = repoID, lang
	if err := NewSQLiteOutcomeWriter(s.db, s.clock).Record(context.Background(), o); err != nil {
		t.Fatalf("record outcome for language lookup: %v", err)
	}
}

// TestFallbackChain: 0-4 repo observations use the lang aggregate, with none
// there global (the prior when empty); 5 use the repo posterior. Each level's
// stored key form is asserted and Posterior reports the level it used.
func TestFallbackChain(t *testing.T) {
	clock := newStepClock()
	s := newTestScorer(t, clock)
	ctx := context.Background()
	tier, tc := capacity.TierOne, conductor.TaskClassCode
	a0, b0, _ := PriorAlphaBeta(tier, tc)
	repo := repoScope("repo-opaque-1")
	recordLangOutcome(t, s, "job-lang-1", "repo-opaque-1", LanguageGo)
	if repo != "repo:repo-opaque-1" || langScope(LanguageGo) != "lang:go" || scopeGlobal != "global" {
		t.Fatalf("key forms = %q %q %q", repo, langScope(LanguageGo), scopeGlobal)
	}
	want := func(step string, level FallbackLevel, obs int, alpha, beta float64) {
		t.Helper()
		p, err := s.Posterior(ctx, repo, tc, tier)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if p.Level != level || p.Observations != obs || !near(p.Alpha, alpha) || !near(p.Beta, beta) {
			t.Errorf("%s: Posterior = %+v, want level %s obs %d alpha %v beta %v", step, p, level, obs, alpha, beta)
		}
	}
	want("nothing stored", LevelGlobal, 0, a0, b0)
	seedScore(t, s, scopeGlobal, tc, tier, 3, 1, 4, clock.t)
	want("global only", LevelGlobal, 4, a0+3, b0+1)
	seedScore(t, s, langScope(LanguageGo), tc, tier, 2, 2, 4, clock.t)
	want("lang present", LevelLang, 4, a0+2, b0+2)
	seedScore(t, s, repo, tc, tier, 4, 0, 4, clock.t)
	want("repo with 4 observations", LevelLang, 4, a0+2, b0+2)
	if _, err := s.db.Exec(`UPDATE `+tableCapabilityScore+` SET observation_count = 5 WHERE scope_key = ?`, string(repo)); err != nil {
		t.Fatal(err)
	}
	want("repo with 5 observations", LevelRepo, 5, a0+4, b0)
	for _, key := range []ScopeKey{repo, langScope(LanguageGo), scopeGlobal} {
		if _, found := readStored(t, s, key, tc, tier); !found {
			t.Errorf("no stored row under key %q", key)
		}
	}
	if p, err := s.Posterior(ctx, langScope(LanguageGo), tc, tier); err != nil || p.Level != LevelLang {
		t.Errorf("lang scope Posterior = %+v, %v, want level lang", p, err)
	}
	if p, err := s.Posterior(ctx, langScope(LanguagePython), tc, tier); err != nil || p.Level != LevelGlobal || p.Observations != 4 {
		t.Errorf("lang scope with no row Posterior = %+v, %v, want the global row", p, err)
	}
	if p, err := s.Posterior(ctx, repoScope("repo-unseen"), tc, tier); err != nil || p.Level != LevelGlobal {
		t.Errorf("repo with no outcome and no row Posterior = %+v, %v, want global", p, err)
	}
}

// TestObservationForMapping: only an outcome that names a scored cell maps to
// an observation; success is exactly final_outcome accepted.
func TestObservationForMapping(t *testing.T) {
	o := baseOutcome("job-map-1")
	scope, tc, tier, success, ok := observationFor(o)
	if !ok || scope != "repo:repo-opaque-1" || tc != conductor.TaskClassCode || tier != capacity.TierOne || !success {
		t.Fatalf("observationFor(accepted) = %q %q %q %v %v", scope, tc, tier, success, ok)
	}
	for outcome, want := range map[OutcomeClass]bool{OutcomeAccepted: true, OutcomeRejected: false, OutcomeUnknown: false} {
		o.FinalOutcome = outcome
		if _, _, _, got, ok := observationFor(o); !ok || got != want {
			t.Errorf("observationFor(%s) success = %v ok = %v, want %v", outcome, got, ok, want)
		}
	}
	for name, mut := range map[string]func(*TelemetryOutcome){
		"unknown repo":   func(x *TelemetryOutcome) { x.RepoID = "unknown" },
		"empty repo":     func(x *TelemetryOutcome) { x.RepoID = "" },
		"unknown tier":   func(x *TelemetryOutcome) { x.LaneTier = "unknown" },
		"no prior class": func(x *TelemetryOutcome) { x.TaskClass = "chat" },
		"bogus class":    func(x *TelemetryOutcome) { x.TaskClass = "bogus" },
	} {
		x := baseOutcome("job-map-2")
		mut(&x)
		if _, _, _, _, ok := observationFor(x); ok {
			t.Errorf("observationFor(%s) ok = true, want false", name)
		}
	}
}

// observeCall is one recorded ObservationWriter.Observe call.
type observeCall struct {
	scope   ScopeKey
	tc      conductor.TaskClass
	tier    capacity.Tier
	success bool
}

// spyObserver records every Observe call and returns err.
type spyObserver struct {
	calls []observeCall
	err   error
}

func (s *spyObserver) Observe(_ context.Context, scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier, success bool) error {
	s.calls = append(s.calls, observeCall{scope, tc, tier, success})
	return s.err
}

// TestObservationUpdatedOnOutcome: Record for a terminal outcome calls
// Observe once for (repo:<id>, task_class, lane_tier) with success = accepted,
// the stored row follows, a repeat Record observes nothing, and a Record that
// fails before the insert, or names no scored cell, observes nothing.
func TestObservationUpdatedOnOutcome(t *testing.T) {
	db, ctx := newTestOutcomeDB(t), context.Background()
	spy := &spyObserver{}
	w := NewSQLiteOutcomeWriter(db, newTestClock()).WithObservationWriter(spy)
	if err := w.Record(ctx, baseOutcome("job-obs-1")); err != nil {
		t.Fatal(err)
	}
	want := observeCall{"repo:repo-opaque-1", conductor.TaskClassCode, capacity.TierOne, true}
	if len(spy.calls) != 1 || spy.calls[0] != want {
		t.Fatalf("Observe calls after an accepted outcome = %+v, want exactly %+v", spy.calls, want)
	}
	rejected := baseOutcome("job-obs-2")
	rejected.FinalOutcome = OutcomeRejected
	if err := w.Record(ctx, rejected); err != nil {
		t.Fatal(err)
	}
	if len(spy.calls) != 2 || spy.calls[1].success {
		t.Fatalf("Observe calls after a rejected outcome = %+v, want a second call with success=false", spy.calls)
	}
	if err := w.Record(ctx, baseOutcome("job-obs-1")); err != nil || len(spy.calls) != 2 {
		t.Fatalf("repeat Record = %v with %d Observe calls, want nil and still 2 (no double count)", err, len(spy.calls))
	}
	bad := baseOutcome("job-obs-3")
	bad.Language = "klingon"
	if err := w.Record(ctx, bad); err == nil || len(spy.calls) != 2 {
		t.Fatalf("invalid Record = %v with %d Observe calls, want an error and still 2", err, len(spy.calls))
	}
	for name, mut := range map[string]func(*TelemetryOutcome){
		"unknown repo": func(x *TelemetryOutcome) { x.RepoID = "unknown" },
		"unknown tier": func(x *TelemetryOutcome) { x.LaneTier = "unknown" },
		"chat class":   func(x *TelemetryOutcome) { x.TaskClass = "chat" },
	} {
		x := baseOutcome("job-obs-skip-" + strings.ReplaceAll(name, " ", "-"))
		mut(&x)
		if err := w.Record(ctx, x); err != nil {
			t.Fatalf("%s: Record = %v, want nil", name, err)
		}
	}
	if len(spy.calls) != 2 {
		t.Errorf("Observe calls after outcomes naming no scored cell = %d, want 2", len(spy.calls))
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tableTelemetryOutcomes).Scan(&n); err != nil || n != 5 {
		t.Errorf("recorded outcome rows = %d (err %v), want 5 (skipped outcomes are still recorded)", n, err)
	}
}

// TestObservationStoredThroughScorer: Record with the real scorer attached
// leaves the decayed row the scorer reads; an Observe error surfaces.
func TestObservationStoredThroughScorer(t *testing.T) {
	clock := newStepClock()
	s := newTestScorer(t, clock)
	w := NewSQLiteOutcomeWriter(s.db, clock).WithObservationWriter(s)
	ctx := context.Background()
	rejected := baseOutcome("job-real-2")
	rejected.FinalOutcome = OutcomeRejected
	for _, o := range []TelemetryOutcome{baseOutcome("job-real-1"), rejected, baseOutcome("job-real-3")} {
		if err := w.Record(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	row, found := readStored(t, s, "repo:repo-opaque-1", conductor.TaskClassCode, capacity.TierOne)
	if !found || row.alpha != 2 || row.beta != 1 || row.count != 3 {
		t.Fatalf("stored row = %+v (found %v), want alpha 2 beta 1 count 3", row, found)
	}
	failing := NewSQLiteOutcomeWriter(s.db, clock).WithObservationWriter(&spyObserver{err: cascade.New(cascade.KindUnavailable, "boom")})
	if err := failing.Record(ctx, baseOutcome("job-real-4")); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("Record with a failing observer = %v, want its KindUnavailable error", err)
	}
}
