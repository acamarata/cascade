package health

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestSchedulerRecoversDegradedProvider composes the production pieces for
// a provider demoted by the transient counter: the scheduler waits out
// DegradedBase between probes, a still-failing probe keeps it degraded, and
// the first healthy probe returns it to healthy with a zero demotion count.
func TestSchedulerRecoversDegradedProvider(t *testing.T) {
	ctx := context.Background()
	clk := testkit.NewFrozenClock(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
	prober := &countingProber{result: ProbeResult{Status: 500, Success: false}}
	mgr, reg, _ := newTestManagerWithEgress(t, clk, 0, testEngine(t), prober)
	seedProvider(t, reg, "p")
	if err := mgr.DemoteProvider(ctx, "p", provider.ReasonTransientUpstream); err != nil {
		t.Fatalf("DemoteProvider: %v", err)
	}
	iv := DefaultIntervals()
	sched := NewScheduler(mgr, reg, clk, iv)

	sched.scanOnce(ctx)
	if prober.calls != 0 {
		t.Fatalf("first scan right after the demotion probed %d times, want 0", prober.calls)
	}
	clk.Advance(iv.DegradedBase - time.Second)
	sched.scanOnce(ctx)
	if prober.calls != 0 {
		t.Fatalf("scan before DegradedBase probed (%d calls)", prober.calls)
	}
	if st, _ := mgr.GetHealth(ctx, "p"); st != provider.HealthDegraded {
		t.Fatalf("health = %q before recovery, want degraded", st)
	}

	prober.result = ProbeResult{Status: 200, Success: true}
	clk.Advance(time.Second)
	sched.scanOnce(ctx)
	if prober.calls != 1 {
		t.Fatalf("scan at DegradedBase probed %d times in total, want 1", prober.calls)
	}
	rec, err := reg.GetProvider(ctx, "p")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if rec.HealthStatus != "healthy" || rec.DemotionCount != 0 {
		t.Fatalf("after recovery: status %q count %d, want healthy and 0", rec.HealthStatus, rec.DemotionCount)
	}
}
