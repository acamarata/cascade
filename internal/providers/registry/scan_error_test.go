// Purpose: the scan-error branches ListProviders/ListLanes/ListPool hit
//   when a row already in the table is malformed -- distinct from
//   GetProvider's own scan-error tests in registry_test.go, since these
//   are separate call sites (registry.go:226, lanes.go:183, pool.go:108)
//   that wrap the same scan failure with their own message.
// Inputs: none beyond the shared newTestRegistry fixture.
// Outputs: none (test file).
// Constraints: corrupts a column via a raw UPDATE, bypassing the
//   Upsert*'s own encoding -- mirrors registry_test.go's
//   TestScanProviderRowDecodeErrors pattern.
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2 coverage).

package registry

import (
	"context"
	"testing"
)

// TestListProvidersScanErrorPropagates corrupts one row's known_models
// column after insertion and proves ListProviders (not just GetProvider)
// surfaces the resulting decode error.
func TestListProvidersScanErrorPropagates(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, sampleProvider("list-broken")); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if _, err := reg.db.ExecContext(ctx,
		`UPDATE `+tableProviderRecords+` SET known_models = ? WHERE name = ?`, "{not-valid-json", "list-broken"); err != nil {
		t.Fatalf("corrupt known_models: %v", err)
	}
	if _, err := reg.ListProviders(ctx); err == nil {
		t.Fatal("ListProviders should fail when a stored row has a malformed known_models column")
	}
}

// TestScanLaneRowDecodeError corrupts a lane's model_filter column
// directly and proves ListLanes and ListPool both surface scanLaneRow's
// JSON-decode error rather than panicking or returning a zero-value lane.
func TestScanLaneRowDecodeError(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertOwningProvider(t, reg, "lane-owner")
	if err := reg.UpsertLane(ctx, LaneRecord{
		LaneName: "broken-lane", ProviderName: "lane-owner", PoolMembership: "p",
		Capacity: CapacityAPICredit, State: LaneStateAvailable,
	}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	if _, err := reg.db.ExecContext(ctx,
		`UPDATE `+tableProviderLanes+` SET model_filter = ? WHERE lane_name = ?`, "{not-valid-json", "broken-lane"); err != nil {
		t.Fatalf("corrupt model_filter: %v", err)
	}

	if _, err := reg.ListLanes(ctx); err == nil {
		t.Fatal("ListLanes should fail when a stored lane has a malformed model_filter column")
	}
	if _, err := reg.ListPool(ctx, "p"); err == nil {
		t.Fatal("ListPool should fail when a stored lane has a malformed model_filter column")
	}
}
