// Purpose: tests for ceiling enforcement on unconfigured spill-order path (AUD-030).
// Inputs: routeSnapshot candidates, QuotaPolicy with CeilingOverrides.
// Outputs: test assertions for TestUnconfiguredSpillOrderEnforcesCeiling.
// Constraints: injected Clock, no sleep, file <= 300 lines, functions <= 50 lines.
// SPORT: conductor.router/AUD-030 (P1-CAP-14).

package conductor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

func TestUnconfiguredSpillOrderEnforcesCeiling(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))

	// With no ceilings, HEAD behavior (first candidate returned) is unchanged.
	testUnconfiguredNoCeilings(t, clock)

	// With ceilings, filterQuota returns first candidate under ceiling,
	// and ErrAllLanesExhausted when every candidate is at its ceiling.
	testUnconfiguredWithCeilings(t, clock)
}

func testUnconfiguredNoCeilings(t *testing.T, clock *testkit.FrozenClock) {
	t.Helper()
	p := NewQuotaPolicy(QuotaConfig{}, clock)
	r := NewRouter(twoLaneRegistry(), p, clock, nil)

	sel, err := r.Select(context.Background(), chatReq())
	if err != nil {
		t.Fatalf("Select: unexpected error: %v", err)
	}
	if sel.LaneID != "lane-local" {
		t.Fatalf("LaneID = %q, want lane-local (first candidate)", sel.LaneID)
	}
	if !containsPrefix(sel.ReasonFlags, "quota:unconfigured-spill-order") {
		t.Fatalf("flags = %v, want quota:unconfigured-spill-order", sel.ReasonFlags)
	}
}

func testUnconfiguredWithCeilings(t *testing.T, clock *testkit.FrozenClock) {
	t.Helper()
	cfg := QuotaConfig{
		CeilingOverrides: map[LaneID]int64{
			"lane-local":  1,
			"lane-remote": 1,
		},
	}
	p := NewQuotaPolicy(cfg, clock)
	r := NewRouter(twoLaneRegistry(), p, clock, nil)
	ctx := context.Background()

	// Call 1: lane-local admitted.
	sel, err := r.Select(ctx, chatReq())
	if err != nil || sel.LaneID != "lane-local" {
		t.Fatalf("call 1: got (%q, %v), want (lane-local, nil)", sel.LaneID, err)
	}

	// Call 2: lane-local is at ceiling (1), so lane-remote is admitted.
	sel, err = r.Select(ctx, chatReq())
	if err != nil || sel.LaneID != "lane-remote" {
		t.Fatalf("call 2: got (%q, %v), want (lane-remote, nil)", sel.LaneID, err)
	}

	// Call 3: both lane-local and lane-remote are at ceiling. ErrAllLanesExhausted expected.
	_, err = r.Select(ctx, chatReq())
	if !errIdentity(err, ErrAllLanesExhausted) {
		t.Fatalf("call 3: got error %v, want ErrAllLanesExhausted (identity)", err)
	}
	if !strings.Contains(err.Error(), "lane-local") || !strings.Contains(err.Error(), "lane-remote") {
		t.Fatalf("call 3: expected message naming both lanes, got %q", err.Error())
	}

	// Advance clock past CeilingWindow: lane-local is admitted again.
	clock.Advance(CeilingWindow + time.Second)
	sel, err = r.Select(ctx, chatReq())
	if err != nil || sel.LaneID != "lane-local" {
		t.Fatalf("call 4 after window: got (%q, %v), want (lane-local, nil)", sel.LaneID, err)
	}
}
