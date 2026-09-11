// Purpose: per-leg field-reset assertions for the fan-out primitive
//   (R-21.214), split out of fanout_test.go to stay under the 300-line
//   file cap. T0 unblock (P1-E11-W3-S23-T2): these were t.Skip stubs
//   until pkg/provider/model.go gained ModelRequest.FanOut and
//   ModelRequest.ReservationID.
// Inputs: none beyond the shared fanoutReq/passthroughPermit/spyJournal
//   helpers declared in fanout_test.go (same package, same test binary).
// Outputs: none.
// Constraints: none beyond the package's own.
// SPORT: conductor.fanout/ADD (P1-E11-W3-S23-T2, T0 unblock).

package conductor

import (
	"context"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestFanOut_LegFanOutResetToOne asserts every dispatched leg sees
// FanOut==1 regardless of the parent template's own FanOut value
// (R-21.214; T0 decision unblocked provider.ModelRequest.FanOut).
func TestFanOut_LegFanOutResetToOne(t *testing.T) {
	req := fanoutReq()
	req.FanOut = 5 // the parent's own leg count, never copied to a leg
	var seen []int
	var mu sync.Mutex
	exec := func(_ context.Context, r provider.ModelRequest) (provider.ModelResponse, error) {
		mu.Lock()
		seen = append(seen, r.FanOut)
		mu.Unlock()
		return provider.ModelResponse{JobID: "job"}, nil
	}
	j := &spyJournal{}
	if _, err := FanOut(context.Background(), req, 3, nil, passthroughPermit, j, exec); err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("legs dispatched = %d, want 3", len(seen))
	}
	for i, fo := range seen {
		if fo != 1 {
			t.Errorf("leg %d: FanOut = %d, want 1", i, fo)
		}
	}
}

// TestFanOut_PerLegReservationCleared asserts every dispatched leg's copy
// has ReservationID cleared, even though the parent template carried one
// (R-21.214: each leg takes its own reservation rather than inheriting the
// parent's; T0 decision unblocked provider.ModelRequest.ReservationID).
func TestFanOut_PerLegReservationCleared(t *testing.T) {
	req := fanoutReq()
	req.ReservationID = "parent-reservation-42"
	var seen []string
	var mu sync.Mutex
	exec := func(_ context.Context, r provider.ModelRequest) (provider.ModelResponse, error) {
		mu.Lock()
		seen = append(seen, r.ReservationID)
		mu.Unlock()
		return provider.ModelResponse{JobID: "job"}, nil
	}
	j := &spyJournal{}
	if _, err := FanOut(context.Background(), req, 2, nil, passthroughPermit, j, exec); err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("legs dispatched = %d, want 2", len(seen))
	}
	for i, rid := range seen {
		if rid != "" {
			t.Errorf("leg %d: ReservationID = %q, want cleared", i, rid)
		}
	}
}
