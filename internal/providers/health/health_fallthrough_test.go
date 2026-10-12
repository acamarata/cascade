package health

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the deterministic fall-through branches of the health manager and
// scheduler: an unknown health status keeps the healthy cadence, and a
// chooser whose resolver finds no topology mapping hands the demotion back to
// the legacy registry path instead of swallowing it.
// SPORT: internal/providers/health.

func TestNextIntervalUnknownStatusKeepsTheHealthyCadence(t *testing.T) {
	iv := DefaultIntervals()
	if got := nextInterval(registry.HealthStatus("not-a-status"), 3, 3, iv); got != iv.Healthy {
		t.Fatalf("unknown status interval = %s, want the healthy %s", got, iv.Healthy)
	}
}

func TestRouteThroughChooserFallsBackWhenNothingResolves(t *testing.T) {
	clk := testkit.NewFrozenClock(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	mgr, reg, _ := newTestManager(t, clk, 3)
	seedProvider(t, reg, "p1")
	fc := &fakeChooserRoute{}
	mgr.WithChooser(fc, func(string) (topology.DomainID, topology.CredentialID, bool) { return "", "", false })
	handled, err := mgr.routeThroughChooser(context.Background(), "p1", provider.ReasonRateLimited429)
	if handled || err != nil {
		t.Fatalf("routeThroughChooser = %v, %v; want unhandled so the legacy path runs", handled, err)
	}
	if len(fc.calls) != 0 {
		t.Fatalf("chooser called without a topology mapping: %+v", fc.calls)
	}
}
