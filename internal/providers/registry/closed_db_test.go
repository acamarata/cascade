// Purpose: exercises the SQL-failure branches most methods in this
//   package share -- a closed *sql.DB (begin-tx/query/exec refuses), a
//   dropped table mid-transaction, and a cost record that cannot marshal
//   -- none of which the happy-path tests in registry_test.go/lanes_test.go
//   /pool_test.go reach.
// Inputs: none beyond the shared newTestRegistry fixture.
// Outputs: none (test file).
// Constraints: white-box (same package); every case asserts a real
//   returned error, never merely "did not panic".
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2/T3 coverage).

package registry

import (
	"context"
	"testing"
	"time"
)

// TestClosedDBFailures drives every write/read method against a Registry
// whose underlying *sql.DB has already been closed, proving each surfaces
// a real error from its begin-tx/query/exec call rather than panicking or
// silently succeeding.
func TestClosedDBFailures(t *testing.T) {
	reg := newTestRegistry(t)
	if err := reg.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	ctx := context.Background()

	if err := reg.UpsertProvider(ctx, sampleProvider("x")); err == nil {
		t.Error("UpsertProvider on a closed db should have failed")
	}
	if _, err := reg.ListProviders(ctx); err == nil {
		t.Error("ListProviders on a closed db should have failed")
	}
	if err := reg.DeleteProvider(ctx, "x"); err == nil {
		t.Error("DeleteProvider on a closed db should have failed at begin tx")
	}
	if _, err := reg.AdvancePoolIndex(ctx, "pool"); err == nil {
		t.Error("AdvancePoolIndex on a closed db should have failed at begin tx")
	}
	if err := reg.UpsertLane(ctx, LaneRecord{
		LaneName: "l", ProviderName: "x", Capacity: CapacityAPICredit, State: LaneStateAvailable,
	}); err == nil {
		t.Error("UpsertLane on a closed db should have failed")
	}
	if _, err := reg.ListLanes(ctx); err == nil {
		t.Error("ListLanes on a closed db should have failed")
	}
	if _, err := reg.ListPool(ctx, "pool"); err == nil {
		t.Error("ListPool on a closed db should have failed")
	}

	reader := NewReader(reg)
	if _, err := reader.ListLanes(ctx); err == nil {
		t.Error("Reader.ListLanes should propagate the underlying closed-db error")
	}
	if _, err := reader.ListPool(ctx, "pool"); err == nil {
		t.Error("Reader.ListPool should propagate the underlying closed-db error")
	}
}

// TestDeleteProviderLaneDeleteFails drops provider_lanes out from under an
// otherwise-healthy Registry so DeleteProvider's FIRST statement (delete
// lanes) fails inside the transaction, distinct from the closed-db case
// above which never reaches any statement.
func TestDeleteProviderLaneDeleteFails(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, sampleProvider("drop-lanes")); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if _, err := reg.db.ExecContext(ctx, `DROP TABLE `+tableProviderLanes); err != nil {
		t.Fatalf("drop lanes table: %v", err)
	}
	if err := reg.DeleteProvider(ctx, "drop-lanes"); err == nil {
		t.Fatal("DeleteProvider should fail when provider_lanes is unavailable")
	}
}

// TestDeleteProviderRecordDeleteFails drops provider_records (leaving
// provider_lanes intact) so DeleteProvider's lane delete succeeds but its
// SECOND statement, the provider row delete, fails -- the other half of
// DeleteProvider's transaction body.
func TestDeleteProviderRecordDeleteFails(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	if _, err := reg.db.ExecContext(ctx, `DROP TABLE `+tableProviderRecords); err != nil {
		t.Fatalf("drop provider_records table: %v", err)
	}
	if err := reg.DeleteProvider(ctx, "anything"); err == nil {
		t.Fatal("DeleteProvider should fail when provider_records is unavailable")
	}
}

// TestUpsertProviderPreservesExplicitCreatedAt proves the CreatedAt.IsZero()
// branch's FALSE arm: a caller-supplied, non-zero CreatedAt is kept as-is
// rather than being overwritten by the clock (only a zero CreatedAt is
// stamped -- the true arm the roundtrip test in registry_test.go already
// covers via a fresh sampleProvider).
func TestUpsertProviderPreservesExplicitCreatedAt(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	explicit := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	rec := sampleProvider("explicit-created")
	rec.CreatedAt = explicit

	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	got, err := reg.GetProvider(ctx, "explicit-created")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if !got.CreatedAt.Equal(explicit) {
		t.Fatalf("UpsertProvider overwrote an explicit non-zero CreatedAt: got %v, want %v", got.CreatedAt, explicit)
	}
}

// TestEncodeCostRecordMarshalFailure forces json.Marshal to fail (a
// time.Time year outside [0,9999] is the one field in CostRecord whose
// encoding/json MarshalJSON can return an error) and proves both
// EncodeCostRecord's own error branch and encodeProviderJSON's propagation
// of it are handled as a cascade error, never a panic, and never partial
// output.
func TestEncodeCostRecordMarshalFailure(t *testing.T) {
	badCost := &CostRecord{
		Models:        map[string]ModelCost{"m": {InputMicroUSDPerToken: 1}},
		LastRefreshed: time.Date(99999, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if _, err := EncodeCostRecord(badCost); err == nil {
		t.Fatal("EncodeCostRecord with an out-of-range year should have failed to marshal")
	}

	reg := newTestRegistry(t)
	rec := sampleProvider("bad-cost")
	rec.Cost = badCost
	if err := reg.UpsertProvider(context.Background(), rec); err == nil {
		t.Fatal("UpsertProvider should propagate encodeProviderJSON's cost-encode failure")
	}
}
