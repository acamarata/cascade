package learn

// Purpose: the production EstimateSource's tests, over real seeded decisions
//   and outcomes in a migrated sqlite file.
// SPORT: internal.learn.SQLiteEstimateSource/TESTED (P1-CAP-03).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
)

// seedRouted records an outcome (queue, duration in ms) and a decision that
// routed its job to tier for class at decidedAt.
func seedRouted(t *testing.T, s *SQLiteCapabilityScorer, jobID, class string, tier capacity.Tier, queueMS, durMS int64, decidedAt time.Time) {
	t.Helper()
	o := baseOutcome(jobID)
	o.TaskClass, o.QueueTimeMS, o.DurationMS = class, queueMS, durMS
	if err := NewSQLiteOutcomeWriter(s.db, s.clock).Record(context.Background(), o); err != nil {
		t.Fatalf("record outcome %s: %v", jobID, err)
	}
	d := testDecision("dec-" + jobID + "-" + decidedAt.Format("150405"))
	d.JobID, d.TaskClass, d.SelectedTier, d.DecidedAt = jobID, class, string(tier), decidedAt
	if err := (SQLiteSchedulerDecisionWriter{}).Record(context.Background(), s.db, d); err != nil {
		t.Fatalf("record decision %s: %v", jobID, err)
	}
}

// TestEstimateSourceFromScheduleDecisions: Estimate returns the observed
// median queue wait and duration for the routed (tier, class); a repeated
// dispatch of one job counts once; other tiers, classes and decisions with no
// outcome are excluded; with no rows every tier gets the same cold start.
func TestEstimateSourceFromScheduleDecisions(t *testing.T) {
	clock := newStepClock()
	s := newTestScorer(t, clock)
	est := NewSQLiteEstimateSource(s.db)
	var _ capacity.EstimateSource = est
	cold := func(tier capacity.Tier) {
		t.Helper()
		if w, d := est.Estimate(tier, "code"); w != 0 || d != coldStartDuration {
			t.Errorf("cold Estimate(%s, code) = (%v, %v), want (0, %v)", tier, w, d, coldStartDuration)
		}
	}
	for _, tier := range []capacity.Tier{capacity.TierZero, capacity.TierOne, capacity.TierTwo, capacity.TierLocal} {
		cold(tier)
	}
	at := clock.t
	seedRouted(t, s, "job-est-1", "code", capacity.TierOne, 100, 1000, at)
	seedRouted(t, s, "job-est-2", "code", capacity.TierOne, 300, 5000, at.Add(time.Minute))
	seedRouted(t, s, "job-est-3", "code", capacity.TierOne, 200, 3000, at.Add(2*time.Minute))
	seedRouted(t, s, "job-est-4", "review", capacity.TierOne, 9000, 90000, at)
	seedRouted(t, s, "job-est-5", "code", capacity.TierTwo, 7000, 70000, at)
	d := testDecision("dec-retry")
	d.JobID, d.TaskClass, d.SelectedTier, d.DecidedAt = "job-est-1", "code", "tier-1", at.Add(3*time.Minute)
	if err := (SQLiteSchedulerDecisionWriter{}).Record(context.Background(), s.db, d); err != nil {
		t.Fatal(err)
	}
	orphan := testDecision("dec-orphan")
	orphan.JobID, orphan.TaskClass, orphan.SelectedTier = "job-no-outcome", "code", "tier-1"
	if err := (SQLiteSchedulerDecisionWriter{}).Record(context.Background(), s.db, orphan); err != nil {
		t.Fatal(err)
	}
	if w, dur := est.Estimate(capacity.TierOne, "code"); w != 200*time.Millisecond || dur != 3000*time.Millisecond {
		t.Errorf("Estimate(tier-1, code) = (%v, %v), want the medians (200ms, 3s)", w, dur)
	}
	if w, dur := est.Estimate(capacity.TierTwo, "code"); w != 7*time.Second || dur != 70*time.Second {
		t.Errorf("Estimate(tier-2, code) = (%v, %v), want (7s, 1m10s)", w, dur)
	}
	cold(capacity.TierZero)
	if w, dur := est.Estimate(capacity.TierOne, "review"); w != 9*time.Second || dur != 90*time.Second {
		t.Errorf("Estimate(tier-1, review) = (%v, %v), want (9s, 1m30s)", w, dur)
	}
}

// TestEstimateSourceMedianAndFailures: an even count averages the two middle
// values, and an unreadable or unconstructed source returns the cold start.
func TestEstimateSourceMedianAndFailures(t *testing.T) {
	if got := medianMillis([]int64{400, 100, 300, 200}); got != 250*time.Millisecond {
		t.Errorf("median of 4 values = %v, want 250ms", got)
	}
	if got := medianMillis([]int64{9}); got != 9*time.Millisecond {
		t.Errorf("median of 1 value = %v, want 9ms", got)
	}
	s := newTestScorer(t, newStepClock())
	est := NewSQLiteEstimateSource(s.db)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if w, d := est.Estimate(capacity.TierOne, "code"); w != 0 || d != coldStartDuration {
		t.Errorf("closed db Estimate = (%v, %v), want the cold start", w, d)
	}
	var nilSrc *SQLiteEstimateSource
	if w, d := nilSrc.Estimate(capacity.TierOne, "code"); w != 0 || d != coldStartDuration {
		t.Errorf("nil source Estimate = (%v, %v), want the cold start", w, d)
	}
}
