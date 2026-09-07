package governor

// Purpose: LadderConfig normalization and pressureToStage's test suite.
// TestPressureToStageMonotonic asserts the pure mapping's ordering
// directly across a dense sweep rather than sampling a few points, per
// this ticket's monotonic-safety requirement.

import (
	"testing"
	"time"
)

func TestNormalizeLadderConfigDefaults(t *testing.T) {
	got := NormalizeLadderConfig(LadderConfig{})
	want := LadderConfig{
		WarnThreshold:     DefaultWarnThreshold,
		CriticalThreshold: DefaultCriticalThreshold,
		HaltThreshold:     DefaultHaltThreshold,
		StepDownDwell:     DefaultStepDownDwell,
		PollHz:            0,
	}
	if got != want {
		t.Fatalf("NormalizeLadderConfig(zero) = %+v, want %+v", got, want)
	}
}

func TestNormalizeLadderConfigNegativeDwellFlattenedToZero(t *testing.T) {
	got := NormalizeLadderConfig(LadderConfig{StepDownDwell: -time.Second})
	if got.StepDownDwell != 0 {
		t.Fatalf("StepDownDwell = %v, want 0 (negative flattened)", got.StepDownDwell)
	}
}

func TestNormalizeLadderConfigEnforcesAscendingOrder(t *testing.T) {
	// Warn above Critical above Halt: every stage should be raised up to
	// the one below it, never left able to skip a stage on the way up.
	got := NormalizeLadderConfig(LadderConfig{
		WarnThreshold:     0.9,
		CriticalThreshold: 0.5,
		HaltThreshold:     0.1,
	})
	if got.CriticalThreshold != 0.9 {
		t.Fatalf("CriticalThreshold = %v, want raised to WarnThreshold 0.9", got.CriticalThreshold)
	}
	if got.HaltThreshold != 0.9 {
		t.Fatalf("HaltThreshold = %v, want raised to the corrected CriticalThreshold 0.9", got.HaltThreshold)
	}
	if got.HaltThreshold < got.CriticalThreshold || got.CriticalThreshold < got.WarnThreshold {
		t.Fatalf("ascending-order invariant violated: %+v", got)
	}
}

func TestLadderPeriodForHzDefaultAndClamp(t *testing.T) {
	if got := ladderPeriodForHz(0); got != time.Second {
		t.Fatalf("ladderPeriodForHz(0) = %v, want 1s (DefaultLadderHz)", got)
	}
	if got := ladderPeriodForHz(-1); got != time.Second {
		t.Fatalf("ladderPeriodForHz(-1) = %v, want 1s (default on negative)", got)
	}
	if got := ladderPeriodForHz(10); got != time.Second {
		t.Fatalf("ladderPeriodForHz(10) = %v, want clamped to MaxLadderHz (1s)", got)
	}
}

// TestPressureToStageMonotonic sweeps pressure from 0 to 1 in fine steps
// and asserts the mapped stage never decreases as pressure increases,
// for several distinct threshold configurations. This is the ladder's
// monotonic-safety proof for its pure stage-mapping half: a worsening
// reading can never map to a less restrictive stage than a better one
// did, under any fixed config.
func TestPressureToStageMonotonic(t *testing.T) {
	configs := []LadderConfig{
		NormalizeLadderConfig(LadderConfig{}),
		NormalizeLadderConfig(LadderConfig{WarnThreshold: 0.1, CriticalThreshold: 0.2, HaltThreshold: 0.3}),
		NormalizeLadderConfig(LadderConfig{WarnThreshold: 0.5, CriticalThreshold: 0.5, HaltThreshold: 0.5}),
	}
	for ci, cfg := range configs {
		prev := StageNormal
		for i := 0; i <= 1000; i++ {
			pressure := float64(i) / 1000.0
			stage := pressureToStage(pressure, cfg)
			if stage < prev {
				t.Fatalf("config %d: pressureToStage(%v) = %v, less than previous stage %v at a lower pressure (monotonicity violated)", ci, pressure, stage, prev)
			}
			prev = stage
		}
		if final := pressureToStage(1.0, cfg); final != StageHalt {
			t.Fatalf("config %d: pressureToStage(1.0) = %v, want StageHalt", ci, final)
		}
		if zero := pressureToStage(0.0, cfg); zero != StageNormal && cfg.WarnThreshold > 0 {
			t.Fatalf("config %d: pressureToStage(0.0) = %v, want StageNormal when WarnThreshold > 0", ci, zero)
		}
	}
}
