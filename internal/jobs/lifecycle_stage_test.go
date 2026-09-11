package jobs

import (
	"errors"
	"testing"
)

// TestLifecycleStages_Order asserts LifecycleStages returns exactly the
// 13 R-16.13 stages in written order.
func TestLifecycleStages_Order(t *testing.T) {
	want := []LifecycleStage{
		StageIntent, StageScope, StagePlan, StageDecomposeLease, StageImplement,
		StageCR, StageQA, StageAdversarial, StageIntegrate, StageCleanNodeCI,
		StageReleaseCDGate, StageAccept, StageLearn,
	}
	got := LifecycleStages()
	if len(got) != len(want) {
		t.Fatalf("LifecycleStages() has %d entries, want %d", len(got), len(want))
	}
	for i, stage := range want {
		if got[i] != stage {
			t.Errorf("LifecycleStages()[%d] = %q, want %q", i, got[i], stage)
		}
	}
}

// TestLifecycleStages_ReturnsACopy asserts mutating the returned slice
// never corrupts the package's own data.
func TestLifecycleStages_ReturnsACopy(t *testing.T) {
	got := LifecycleStages()
	got[0] = "corrupted"
	again := LifecycleStages()
	if again[0] != StageIntent {
		t.Fatalf("LifecycleStages() is not defensively copied: got %q after mutating a prior result", again[0])
	}
}

// TestReclassificationStages asserts ReclassificationStages returns
// exactly {StagePlan, StageImplement, StageAccept} in sequence order --
// R-21.146's plan-time, every-lease-checkpoint and pre-acceptance
// points.
func TestReclassificationStages(t *testing.T) {
	want := []LifecycleStage{StagePlan, StageImplement, StageAccept}
	got := ReclassificationStages()
	if len(got) != len(want) {
		t.Fatalf("ReclassificationStages() has %d entries, want %d", len(got), len(want))
	}
	for i, stage := range want {
		if got[i] != stage {
			t.Errorf("ReclassificationStages()[%d] = %q, want %q", i, got[i], stage)
		}
	}
}

// TestParseLifecycleStage_Valid asserts every one of the 13 stages
// round-trips through ParseLifecycleStage.
func TestParseLifecycleStage_Valid(t *testing.T) {
	for _, stage := range LifecycleStages() {
		got, err := ParseLifecycleStage(string(stage))
		if err != nil {
			t.Fatalf("ParseLifecycleStage(%q): unexpected error %v", stage, err)
		}
		if got != stage {
			t.Errorf("ParseLifecycleStage(%q) = %q, want %q", stage, got, stage)
		}
	}
}

// TestParseLifecycleStage_Unknown asserts a fail-closed refusal, never
// a permissive zero-value stage.
func TestParseLifecycleStage_Unknown(t *testing.T) {
	for _, raw := range []string{"", "INTENT", "bogus", "plan "} {
		stage, err := ParseLifecycleStage(raw)
		if !errors.Is(err, ErrUnknownLifecycleStage) {
			t.Errorf("ParseLifecycleStage(%q): got err %v, want ErrUnknownLifecycleStage", raw, err)
		}
		if stage != "" {
			t.Errorf("ParseLifecycleStage(%q): got non-empty stage %q alongside an error", raw, stage)
		}
	}
}
