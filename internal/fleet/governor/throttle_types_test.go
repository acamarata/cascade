package governor

// Purpose: ThrottleEvent and PressureSource's test suite. These are pure
// vocabulary types with no behavior of their own; the tests here confirm
// their shape (value semantics, interface satisfaction) rather than any
// evaluator logic, which throttle_test.go covers.

import (
	"testing"
	"time"
)

// stubPressureSource is a minimal PressureSource for tests that need one
// without pulling in a full AdmissionController.
type stubPressureSource struct{ p float64 }

func (s stubPressureSource) Pressure() float64 { return s.p }

var _ PressureSource = stubPressureSource{}
var _ PressureSource = (*AdmissionController)(nil)

func TestThrottleEventIsValueTyped(t *testing.T) {
	now := time.Unix(1000, 0)
	original := ThrottleEvent{Stage: StageWarn, PreviousStage: StageNormal, Pressure: 0.65, At: now}
	copyEv := original
	copyEv.Stage = StageCritical
	copyEv.Pressure = 0.9

	if original.Stage != StageWarn {
		t.Fatalf("original.Stage mutated to %v via a copy, want StageWarn unchanged", original.Stage)
	}
	if original.Pressure != 0.65 {
		t.Fatalf("original.Pressure mutated to %v via a copy, want 0.65 unchanged", original.Pressure)
	}
	if copyEv.PreviousStage != StageNormal {
		t.Fatalf("copyEv.PreviousStage = %v, want StageNormal (copied field)", copyEv.PreviousStage)
	}
}

func TestPressureSourceInterfaceSatisfaction(t *testing.T) {
	var src PressureSource = stubPressureSource{p: 0.42}
	if got := src.Pressure(); got != 0.42 {
		t.Fatalf("stubPressureSource.Pressure() = %v, want 0.42", got)
	}
}
