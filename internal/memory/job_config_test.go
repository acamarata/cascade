package memory

// Purpose: the review digest's cadence key under test — that an absent key
//   resolves to the shipped weekly default, that a configured cadence
//   moves the SCHEDULE and the WINDOW together (they are derived from one
//   value precisely so they cannot disagree), and that a misconfigured
//   cadence is a hard refusal rather than a silently disabled digest.
// SPORT: internal.memory.JobConfig (CHANGED, P1-E07-W2-S14-T3).

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDefaultJobConfigSchedulesTheDigestWeekly(t *testing.T) {
	got := DefaultJobConfig()
	if got.ReviewDigestCadence != 7*24*time.Hour {
		t.Errorf("cadence = %v, want a week", got.ReviewDigestCadence)
	}
	if got.ReviewDigestSchedule != "@every 168h0m0s" {
		t.Errorf("schedule = %q, want the weekly cron spec", got.ReviewDigestSchedule)
	}
}

// TestReviewCadenceMovesScheduleAndWindowTogether is the whole reason the
// cadence is one key: a digest whose window is shorter than its period
// would silently skip promotions, and a user cannot configure that here
// because there is nothing to configure separately.
func TestReviewCadenceMovesScheduleAndWindowTogether(t *testing.T) {
	for name, value := range map[string]any{"int": 3, "int64": int64(3)} {
		got, err := ParseJobConfig(map[string]any{ConfigKeyReviewCadenceDays: value})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.ReviewDigestCadence != 72*time.Hour {
			t.Errorf("%s: cadence = %v, want 3 days", name, got.ReviewDigestCadence)
		}
		if got.ReviewDigestSchedule != "@every 72h0m0s" {
			t.Errorf("%s: schedule = %q, want the same 3 days", name, got.ReviewDigestSchedule)
		}
	}
}

func TestReviewCadenceRefusesWhatItCannotRun(t *testing.T) {
	for name, value := range map[string]any{
		"not a number": "weekly",
		"a float":      7.5,
		"zero":         int64(0),
		"negative":     -1,
	} {
		got, err := ParseJobConfig(map[string]any{ConfigKeyReviewCadenceDays: value})
		if err == nil {
			t.Errorf("%s: accepted, want a refusal", name)
			continue
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("%s: kind = %v (ok=%v), want KindInvalidInput", name, kind, ok)
		}
		if got.ReviewDigestSchedule != DefaultJobConfig().ReviewDigestSchedule {
			t.Errorf("%s: a refusal returned a half-applied config: %+v", name, got)
		}
	}
}
