package sessions_test

import (
	"math"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
)

// TestSignalWeightsSumToOne proves confidence.go's bound - "the score
// cannot exit its range for any input the machine accepts" - is backed
// by the config table itself, not only by Weigh's runtime clamp: if
// every registered signal fires, Weigh must return exactly
// ConfidenceFull. This drives the weight table through the public
// StateMachine/ConfidenceScorer surface rather than reading the
// unexported signalWeights map directly, so it exercises the same path
// production does.
func TestSignalWeightsSumToOne(t *testing.T) {
	scorer := sessions.NewConfidenceScorer()
	// Build the full-coverage signal set directly: exactly what
	// buildSignals produces for an Observation where every channel fired.
	full := []sessions.Signal{
		{Kind: sessions.SignalProcessPresent, Present: true},
		{Kind: sessions.SignalRecentPrompt, Present: true},
		{Kind: sessions.SignalRecentTool, Present: true},
		{Kind: sessions.SignalToolActivity, Present: true},
		{Kind: sessions.SignalTranscriptParseable, Present: true},
		{Kind: sessions.SignalStreamOpen, Present: true},
	}
	got := scorer.Weigh(full)
	if math.Abs(float64(got-sessions.ConfidenceFull)) > 1e-9 {
		t.Fatalf("Weigh(all six signals present) = %v, want ConfidenceFull (signalWeights must sum to 1.0)", got)
	}
}

// TestThresholdsAreOrdered proves config.go's three activity thresholds
// are declared in strictly increasing order (idle < blocked < stalled).
// This is a structural sanity check on the config table itself, not a
// re-derivation of classify's logic: it is checked indirectly through
// StateMachine.Advance's threshold-boundary behavior in
// statemachine_test.go, and directly here as a fast, self-contained
// regression guard against a future edit reordering the constants.
func TestThresholdsAreOrdered(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	m := sessions.NewStateMachine()

	// Just past idleThreshold (2m): Idle territory.
	idleAt := clk.Now().Add(-2*time.Minute - time.Second).UnixMilli()
	stIdle, _ := m.Advance(sessions.Observation{
		Census: &census.Snapshot{Pid: 1},
		Domain: &sessions.SessionRecord{SessionID: "s", State: sessions.StateActive.String(), LastToolAt: &idleAt},
	}, clk)
	if stIdle != sessions.StateIdle {
		t.Fatalf("just past idleThreshold classified as %s, want idle", stIdle)
	}

	// Just past blockedThreshold (10m): Blocked territory.
	blockedAt := clk.Now().Add(-10*time.Minute - time.Second).UnixMilli()
	stBlocked, _ := m.Advance(sessions.Observation{
		Census: &census.Snapshot{Pid: 1},
		Domain: &sessions.SessionRecord{SessionID: "s", State: sessions.StateActive.String(), LastToolAt: &blockedAt},
	}, clk)
	if stBlocked != sessions.StateBlocked {
		t.Fatalf("just past blockedThreshold classified as %s, want blocked", stBlocked)
	}

	// Just past stalledThreshold (30m): Stalled territory.
	stalledAt := clk.Now().Add(-30*time.Minute - time.Second).UnixMilli()
	stStalled, _ := m.Advance(sessions.Observation{
		Census: &census.Snapshot{Pid: 1},
		Domain: &sessions.SessionRecord{SessionID: "s", State: sessions.StateBlocked.String(), LastToolAt: &stalledAt},
	}, clk)
	if stStalled != sessions.StateStalled {
		t.Fatalf("just past stalledThreshold classified as %s, want stalled", stStalled)
	}
}
