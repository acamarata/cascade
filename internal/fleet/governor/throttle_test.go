package governor

// Purpose: ThrottleLadder's evaluator test suite. Every test drives tick()
// or evaluateLocked directly against a FixedClock and a stub/real
// PressureSource - never a real sleep (R-14.136, Art.7.3). Subscription
// behavior lives in throttle_subscribe_test.go.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

func newTestLadder(cfg LadderConfig, source PressureSource, clk *runtime.FixedClock) *ThrottleLadder {
	return NewThrottleLadder(cfg, source, clk, newFakeTicker(), nil)
}

// TestThrottleLadderNilSourceFailsClosed asserts the missing-signal case:
// Stage() reports StageHalt unconditionally, before any tick, and stays
// there no matter how many (no-op) ticks run or how far the clock moves -
// there is no signal that could ever justify a lower stage.
func TestThrottleLadderNilSourceFailsClosed(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	l := newTestLadder(LadderConfig{}, nil, clk)

	if got := l.Stage(); got != StageHalt {
		t.Fatalf("Stage() with nil source before any tick = %v, want StageHalt", got)
	}
	for i := 0; i < 5; i++ {
		l.tick()
		clk.Advance(time.Hour)
		if got := l.Stage(); got != StageHalt {
			t.Fatalf("Stage() with nil source after tick %d = %v, want StageHalt", i, got)
		}
	}
}

// TestThrottleLadderEscalatesImmediately asserts a worsening reading
// takes effect on the very next tick, with no dwell requirement.
func TestThrottleLadderEscalatesImmediately(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	cfg := NormalizeLadderConfig(LadderConfig{StepDownDwell: time.Minute})
	l := newTestLadder(cfg, src, clk)

	src.p = 0.99
	l.tick()
	if got := l.Stage(); got != StageHalt {
		t.Fatalf("Stage() after one tick at pressure 0.99 = %v, want StageHalt immediately", got)
	}
}

// TestThrottleLadderDeescalatesOnlyAfterDwell asserts sustained relief is
// required: a single good tick does not step the stage down, but
// continuous relief for StepDownDwell does, one rung at a time.
func TestThrottleLadderDeescalatesOnlyAfterDwell(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	cfg := NormalizeLadderConfig(LadderConfig{StepDownDwell: 30 * time.Second})
	l := newTestLadder(cfg, src, clk)

	src.p = 0.99 // StageHalt
	l.tick()
	if got := l.Stage(); got != StageHalt {
		t.Fatalf("Stage() = %v, want StageHalt after escalation", got)
	}

	src.p = 0.0 // relief all the way to StageNormal territory
	l.tick()
	if got := l.Stage(); got != StageHalt {
		t.Fatalf("Stage() immediately after one relieved tick = %v, want still StageHalt (dwell not elapsed)", got)
	}

	clk.Advance(29 * time.Second)
	l.tick()
	if got := l.Stage(); got != StageHalt {
		t.Fatalf("Stage() just before dwell elapses = %v, want still StageHalt", got)
	}

	clk.Advance(2 * time.Second) // now >= 30s since relief began
	l.tick()
	if got := l.Stage(); got != StageCritical {
		t.Fatalf("Stage() after dwell elapses = %v, want StageCritical (one rung down, not straight to StageNormal)", got)
	}
}

// TestThrottleLadderNoDwellStepsDownImmediately asserts a negative
// StepDownDwell - explicitly "no dwell", normalized to zero by
// NormalizeLadderConfig - is a valid degenerate configuration that
// steps down on the very next relieved tick, not an error. Zero itself
// no longer means this: an unconfigured (zero) StepDownDwell now
// defaults to DefaultStepDownDwell, so "no dwell" must be spelled with
// a negative value instead.
func TestThrottleLadderNoDwellStepsDownImmediately(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	// Passed unnormalized: newTestLadder's NewThrottleLadder normalizes
	// once. Pre-normalizing here too would flatten -time.Second to 0 and
	// then re-normalize that 0 into DefaultStepDownDwell on the second
	// pass, silently losing the "no dwell" configuration this test needs.
	l := newTestLadder(LadderConfig{StepDownDwell: -time.Second}, src, clk)

	src.p = 0.99
	l.tick()
	src.p = 0.0
	l.tick()
	if got := l.Stage(); got != StageCritical {
		t.Fatalf("Stage() with no dwell after one relieved tick = %v, want StageCritical (immediate one-rung step-down)", got)
	}
}

// TestThrottleLadderRapidOscillationDoesNotFlap asserts a signal
// hovering at a boundary, alternating every tick between implying the
// current stage and implying relief, never actually steps down: the
// dwell timer resets every time relief is interrupted, per
// evaluateLocked's "pending != target resets pendingSince" rule.
func TestThrottleLadderRapidOscillationDoesNotFlap(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	cfg := NormalizeLadderConfig(LadderConfig{StepDownDwell: 10 * time.Second})
	l := newTestLadder(cfg, src, clk)

	src.p = 0.99
	l.tick() // StageHalt
	for i := 0; i < 50; i++ {
		clk.Advance(time.Second)
		if i%2 == 0 {
			src.p = 0.0 // relief
		} else {
			src.p = 0.99 // back to worst, interrupts the dwell every time
		}
		l.tick()
	}
	if got := l.Stage(); got != StageHalt {
		t.Fatalf("Stage() after 50 ticks oscillating with dwell never fully elapsed = %v, want still StageHalt (no flap)", got)
	}
}

// TestThrottleLadderMonotonicSafetyFrozenClock is the evaluator's
// monotonic-safety proof: with the clock frozen (so no dwell can ever
// elapse and no de-escalation can ever actually apply), the resulting
// stage after any sequence of pressure readings must equal the running
// maximum of pressureToStage over that sequence - a worsening reading
// never loosens a rung, and with no time passing an improving reading
// can never have tightened one either.
func TestThrottleLadderMonotonicSafetyFrozenClock(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	src := &stubPressureSource{}
	cfg := NormalizeLadderConfig(LadderConfig{StepDownDwell: time.Hour})
	l := newTestLadder(cfg, src, clk)

	pressures := []float64{0.9, 0.1, 0.99, 0.0, 0.5, 0.2, 0.85, 0.0, 1.0, 0.3}
	wantMax := StageNormal
	for i, p := range pressures {
		src.p = p
		l.tick()
		if target := pressureToStage(p, cfg); target > wantMax {
			wantMax = target
		}
		if got := l.Stage(); got != wantMax {
			t.Fatalf("tick %d: Stage() = %v, want running max %v (frozen clock forbids any step-down)", i, got, wantMax)
		}
	}
}

// TestThrottleLadderConsumesPressure proves the ladder's only pressure
// input is AdmissionController.Pressure() (R-21.215): it never derives a
// pressure signal of its own, and every tick's stage tracks exactly what
// pressureToStage says about the controller's current Pressure().
func TestThrottleLadderConsumesPressure(t *testing.T) {
	// MaxInflight is 2, not 4, because this test takes exactly TWO permits
	// before expecting the next request to QUEUE. At 4 the third request
	// was granted outright, so enqueueAndWait waited for an enqueue that
	// never happened -- which, before that helper's wait was bounded, hung
	// the entire package for its full 10-minute timeout and destroyed every
	// other governor test's result along with it.
	ac := newTestController(AdmissionConfig{MaxInflight: 2, QueueCap: 4}, ResourceSnapshot{})
	clk := runtime.NewFixedClock(time.Unix(0, 0))
	cfg := NormalizeLadderConfig(LadderConfig{StepDownDwell: -time.Second})
	l := newTestLadder(cfg, ac, clk)

	if p := ac.Pressure(); p != 0 {
		t.Fatalf("precondition: idle controller Pressure() = %v, want 0", p)
	}
	l.tick()
	if got := l.Stage(); got != StageNormal {
		t.Fatalf("Stage() at idle Pressure()=0 = %v, want StageNormal", got)
	}

	permit1, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	permit2, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	res := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})

	wantStage := pressureToStage(ac.Pressure(), cfg)
	l.tick()
	if got := l.Stage(); got != wantStage {
		t.Fatalf("Stage() at Pressure()=%v = %v, want %v (pressureToStage of the controller's own metric)", ac.Pressure(), got, wantStage)
	}

	permit1.Release()
	got := <-res
	if got.err == nil {
		got.permit.Release()
	}
	permit2.Release()
}

// TestThrottleLadderStageInstalledAsAdmissionStageProvider is this
// ticket's wiring proof: ac.SetStageProvider(l.Stage) - the exact call a
// composition root makes, requiring no change to admission.go's already
// generic seam - makes Admit respect the ladder's real, pressure-derived
// stage rather than a hand-written stub closure.
func TestThrottleLadderStageInstalledAsAdmissionStageProvider(t *testing.T) {
	ac := newTestController(AdmissionConfig{MaxInflight: 4, QueueCap: 4}, ResourceSnapshot{})
	clk := runtime.NewFixedClock(time.Unix(0, 0))

	// The ladder here is driven by a stub source rather than ac's own
	// Pressure(), so the test can force each stage deterministically
	// without needing to fill the real admission queue; the wiring under
	// test - ac.SetStageProvider(l2.Stage) - is exactly the call a
	// composition root makes once a real ladder reads ac.Pressure().
	//
	// Passed unnormalized (see TestThrottleLadderNoDwellStepsDownImmediately):
	// newTestLadder normalizes once, and pre-normalizing here too would
	// turn "no dwell" into DefaultStepDownDwell on the second pass.
	src := &stubPressureSource{p: 0.99}
	l2 := newTestLadder(LadderConfig{StepDownDwell: -time.Second}, src, clk)
	ac.SetStageProvider(l2.Stage)
	l2.tick()
	if got := l2.Stage(); got != StageHalt {
		t.Fatalf("precondition: l2.Stage() = %v, want StageHalt", got)
	}

	if _, err := ac.Admit(context.Background(), AdmissionRequest{}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("Admit with installed StageHalt ladder = %v, want ErrThrottled", err)
	}
	if ac.QueueDepth() != 0 {
		t.Fatal("StageHalt via installed ladder must queue nothing")
	}

	src.p = 0.85 // StageCritical
	l2.tick()
	permit1, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("first Admit at StageCritical (effective MaxInflight=2): %v", err)
	}
	permit2, err := ac.Admit(context.Background(), AdmissionRequest{})
	if err != nil {
		t.Fatalf("second Admit at StageCritical: %v", err)
	}
	res := enqueueAndWait(context.Background(), t, ac, AdmissionRequest{})
	if ac.QueueDepth() != 1 {
		t.Fatal("a third request must queue once the halved effective MaxInflight (2) is full")
	}
	permit1.Release()
	got := <-res
	if got.err != nil {
		t.Fatalf("third Admit after release: %v", got.err)
	}
	permit2.Release()
	got.permit.Release()
}
