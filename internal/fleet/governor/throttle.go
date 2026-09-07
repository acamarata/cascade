// Package governor (throttle.go) implements ThrottleLadder: the graduated
// pressure-response ladder built on top of S-26.T2's AdmissionController
// (P1-E13-W3-S26-T3).
//
// The ladder polls AdmissionController.Pressure() - its only input,
// R-21.215 - on the injected clock and maps it to one of four stages
// (normal < warn < critical < halt). Escalation (a worsening reading)
// applies immediately; de-escalation requires the reading to imply a
// lower stage continuously for LadderConfig.StepDownDwell before the
// ladder actually steps down, one rung at a time (hysteresis - sustained
// relief, never a single good tick, earns a step down).
//
// FAIL CLOSED: a nil PressureSource is a missing signal, not a zero
// reading. Stage() reports StageHalt unconditionally in that case, from
// construction onward, with no tick required and no path back to a
// lower stage - there is nothing to sample that could ever justify one.
// This mirrors AdmissionController's own nil-Sampler refusal and must
// never be collapsed with a real zero-valued Pressure() reading, which
// is StageNormal like any other low reading.
//
// SPORT: internal/fleet/governor.ThrottleLadder (ADD, per T-3
// sport_updates).
package governor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// ThrottleLadder polls a PressureSource on an injected clock and reports
// the pressure stage it implies, publishing every transition to its
// subscribers (Subscribe). The zero value is not usable; construct with
// NewThrottleLadder.
type ThrottleLadder struct {
	cfg    LadderConfig
	source PressureSource
	clk    runtime.Clock
	ticker runtime.Ticker
	log    *slog.Logger

	mu           sync.Mutex
	currentStage ThrottleStage
	pending      ThrottleStage
	pendingSince time.Time
	hasPending   bool

	subMu   sync.Mutex
	subs    map[int]*subscriber
	nextSub int

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	// afterTick is a test-only hook invoked once per tick, after any
	// transition has been published. Always nil in production; gives a
	// test driving Start's real goroutine a deterministic, non-sleep
	// signal that one tick has completed (mirrors sampler.go's
	// afterTick, R-14.136).
	afterTick func()
}

// NewThrottleLadder builds a ThrottleLadder reading pressure from source.
// source may be nil only to exercise the fail-closed path (Stage()
// reports StageHalt unconditionally); production always supplies a live
// PressureSource, typically an *AdmissionController. A nil clk falls
// back to runtime.NewSystemClock(); a nil ticker falls back to
// runtime.NewSystemTicker(ladderPeriodForHz(cfg.PollHz)) - tests should
// always inject a fake Ticker instead of relying on this fallback
// (R-14.136). A nil log discards nothing here (the ladder never logs a
// failure of its own; PressureSource.Pressure cannot itself fail) but is
// accepted for symmetry with NewSampler/NewAdmissionController and
// reserved for future diagnostic logging.
func NewThrottleLadder(cfg LadderConfig, source PressureSource, clk runtime.Clock, ticker runtime.Ticker, log *slog.Logger) *ThrottleLadder {
	cfg = NormalizeLadderConfig(cfg)
	if clk == nil {
		clk = runtime.NewSystemClock()
	}
	if ticker == nil {
		ticker = runtime.NewSystemTicker(ladderPeriodForHz(cfg.PollHz))
	}
	return &ThrottleLadder{
		cfg:    cfg,
		source: source,
		clk:    clk,
		ticker: ticker,
		log:    log,
		subs:   make(map[int]*subscriber),
	}
}

// Start launches the ladder's background goroutine, which ticks until
// ctx is cancelled or Stop is called (whichever comes first), then stops
// the ticker and returns. Start itself does not block.
func (l *ThrottleLadder) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	l.cancelMu.Lock()
	l.cancel = cancel
	l.cancelMu.Unlock()
	go l.run(runCtx)
}

// Stop cancels the ladder's run loop. Safe to call more than once, and
// safe to call before Start (a no-op in that case): context.CancelFunc
// is itself idempotent.
func (l *ThrottleLadder) Stop() {
	l.cancelMu.Lock()
	cancel := l.cancel
	l.cancelMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// run is the ladder's single background goroutine body.
func (l *ThrottleLadder) run(ctx context.Context) {
	defer l.ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.ticker.C():
			l.tick()
		}
	}
}

// Stage returns the ladder's current throttle stage. With a nil
// PressureSource this unconditionally reports StageHalt - the missing
// signal case - regardless of whether Start has ever ticked. Otherwise
// it returns the last stage a completed tick produced, StageNormal
// before the first tick.
func (l *ThrottleLadder) Stage() ThrottleStage {
	if l.source == nil {
		return StageHalt
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.currentStage
}

// tick collects one pressure reading and evaluates it. A nil source
// contributes nothing to evaluate - Stage() already reports StageHalt
// unconditionally in that case, so there is no transition to compute or
// publish, only the test hook to fire.
func (l *ThrottleLadder) tick() {
	if l.source == nil {
		if l.afterTick != nil {
			l.afterTick()
		}
		return
	}
	pressure := l.source.Pressure()
	now := l.clk.Now()
	l.mu.Lock()
	prev := l.currentStage
	next := l.evaluateLocked(pressure, now)
	l.mu.Unlock()
	if next != prev {
		l.publish(ThrottleEvent{Stage: next, PreviousStage: prev, Pressure: pressure, At: now})
	}
	if l.afterTick != nil {
		l.afterTick()
	}
}

// evaluateLocked applies one pressure reading against the current stage
// with hysteresis and returns the resulting stage. Callers must hold
// l.mu. Escalation (target above current) is immediate. De-escalation
// (target below current) requires the reading to have implied a lower
// stage continuously, without an intervening escalation or a return to
// the current stage, for cfg.StepDownDwell before the ladder actually
// steps down - and then only by one rung, so sustained relief that
// spans multiple rungs is earned one dwell window at a time.
func (l *ThrottleLadder) evaluateLocked(pressure float64, now time.Time) ThrottleStage {
	target := pressureToStage(pressure, l.cfg)
	switch {
	case target > l.currentStage:
		l.currentStage = target
		l.hasPending = false
		return l.currentStage
	case target >= l.currentStage:
		l.hasPending = false
		return l.currentStage
	}
	if !l.hasPending || l.pending != target {
		l.pending = target
		l.pendingSince = now
		l.hasPending = true
	}
	if now.Sub(l.pendingSince) >= l.cfg.StepDownDwell {
		l.currentStage--
		l.pendingSince = now
	}
	return l.currentStage
}
