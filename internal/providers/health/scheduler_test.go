package health

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestBackoffSchedule: a frozen-clock table asserting >=4 doubling steps
// for both the degraded and dead interval schedules, against a golden
// fixture.
func TestBackoffSchedule(t *testing.T) {
	iv := DefaultIntervals()
	type row struct {
		Status string        `json:"status"`
		Count  int           `json:"count"`
		Want   time.Duration `json:"want_seconds"`
	}
	rows := []row{
		{"degraded", 1, iv.DegradedBase},
		{"degraded", 2, iv.DegradedBase * 2},
		{"degraded", 3, iv.DegradedBase * 4},
		{"degraded", 4, iv.DegradedBase * 8},
		{"dead", 0, iv.DeadBase},
		{"dead", 1, iv.DeadBase * 2},
		{"dead", 2, iv.DeadMax},  // DeadBase*4 == DeadMax exactly
		{"dead", 3, iv.DeadMax},  // DeadBase*8 would exceed DeadMax; capped
		{"dead", 20, iv.DeadMax}, // capped
	}
	type golden struct {
		Status      string `json:"status"`
		Count       int    `json:"count"`
		WantSeconds int64  `json:"want_seconds"`
	}
	var got []golden
	for _, r := range rows {
		var status registry.HealthStatus
		var interval time.Duration
		switch r.Status {
		case "degraded":
			status = registry.HealthDegraded
			interval = nextInterval(status, r.Count, 0, iv)
		case "dead":
			status = registry.HealthDead
			interval = nextInterval(status, 0, r.Count, iv)
		}
		wantSeconds := int64(r.Want / time.Second)
		if int64(interval/time.Second) != wantSeconds {
			t.Fatalf("%s count=%d: got=%s want=%s", r.Status, r.Count, interval, r.Want)
		}
		got = append(got, golden{Status: r.Status, Count: r.Count, WantSeconds: wantSeconds})
	}
	assertGolden(t, "testdata/backoff_schedule.golden.json", got)
}

func TestClampShiftBounds(t *testing.T) {
	if got := clampShift(-1); got != 0 {
		t.Fatalf("clampShift(-1)=%d want 0", got)
	}
	if got := clampShift(maxBackoffShift + 50); got != maxBackoffShift {
		t.Fatalf("clampShift(overflow)=%d want %d", got, maxBackoffShift)
	}
}

// TestSchedulerRunTicksAtLeastOnce lets Run's timer fire (a short
// scanEvery) before canceling, exercising waitOrDone's timer-fired branch
// (TestSchedulerRunCancelsPromptly only exercises the ctx.Done() branch).
func TestSchedulerRunTicksAtLeastOnce(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: true}}
	eng := testEngine(t)
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	seedProvider(t, reg, "p1")
	sched := NewScheduler(mgr, reg, clk, DefaultIntervals())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	time.Sleep(60 * time.Millisecond) // let at least one tick fire
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Scheduler.Run did not return after cancellation")
	}
}

func TestNextIntervalHealthyIsConstant(t *testing.T) {
	iv := DefaultIntervals()
	for _, count := range []int{0, 1, 5, 100} {
		if got := nextInterval(registry.HealthHealthy, count, count, iv); got != iv.Healthy {
			t.Fatalf("healthy interval varies with count=%d: got %s want %s", count, got, iv.Healthy)
		}
	}
}

// TestSchedulerProbesDueProvidersAndReschedules drives the real Scheduler
// goroutine end to end: a degraded provider due now gets RecoverProbe'd
// exactly once per scan, and a not-yet-due provider is skipped.
func TestSchedulerProbesDueProvidersAndReschedules(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: true}}
	eng := testEngine(t)
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	ctx := context.Background()

	seedProvider(t, reg, "p1")
	if err := mgr.DemoteProvider(ctx, "p1", provider.ReasonRateLimited429); err != nil {
		t.Fatalf("seed demotion: %v", err)
	}

	sched := NewScheduler(mgr, reg, clk, DefaultIntervals())
	sched.scanOnce(ctx)
	if prober.calls != 1 {
		t.Fatalf("first scan: Prober called %d times, want 1", prober.calls)
	}
	// Provider just recovered to healthy; a second immediate scan should
	// not re-probe it (healthy interval has not elapsed).
	sched.scanOnce(ctx)
	if prober.calls != 1 {
		t.Fatalf("second immediate scan: Prober called %d times, want still 1 (not yet due)", prober.calls)
	}
}

// TestSchedulerRunCancelsPromptly proves Run's cancellation path: a
// canceled context stops Run without waiting for scanEvery to elapse.
func TestSchedulerRunCancelsPromptly(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Now())
	prober := &countingProber{result: ProbeResult{Success: true}}
	eng := testEngine(t)
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 3, eng, prober)
	sched := NewScheduler(mgr, reg, clk, DefaultIntervals())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		sched.Run(ctx, time.Hour) // long tick: cancellation must still return promptly
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Scheduler.Run did not return promptly after context cancellation")
	}
}
