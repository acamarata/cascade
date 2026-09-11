package registry

// Purpose: the refusal paths the happy-path CRUD tests never reach. A
// closed handle must refuse every surface as KindUnavailable rather than
// return a zero value that looks like success; a database trigger
// refusing the lane delete, the provider delete, or the pool-index update
// must surface the failure inside the transaction instead of claiming the
// operation succeeded.

import (
	"context"
	"database/sql"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// mustExecTrigger runs one setup statement against db and fails loudly.
// Used only for CREATE TRIGGER fixtures that make a later write refuse.
func mustExecTrigger(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query); err != nil {
		t.Fatalf("exec trigger fixture: %v", err)
	}
}

// TestClosedHandleRefusesEverySurface closes the database after seeding
// one provider and one pool lane, then requires every write, read, and
// Reader surface to refuse with a taxonomy error.
func TestClosedHandleRefusesEverySurface(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertSeededProvider(t, reg, "one")
	if err := reg.UpsertLane(ctx, LaneRecord{
		LaneName: "one", ProviderName: "one", PoolMembership: "p",
		Capacity: CapacityAPICredit, State: LaneStateAvailable,
	}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	reader := NewReader(reg)
	if err := reg.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ops := []struct {
		name string
		call func() error
	}{
		{"UpsertProvider", func() error { return reg.UpsertProvider(ctx, sampleProvider("two")) }},
		{"UpsertLane", func() error {
			return reg.UpsertLane(ctx, LaneRecord{LaneName: "two", ProviderName: "two",
				Capacity: CapacityAPICredit, State: LaneStateUnknown})
		}},
		{"GetProvider", func() error { _, err := reg.GetProvider(ctx, "one"); return err }},
		{"ListProviders", func() error { _, err := reg.ListProviders(ctx); return err }},
		{"GetByModel", func() error { _, err := reg.GetByModel(ctx, "claude-3-5-sonnet-20241022"); return err }},
		{"DeleteProvider", func() error { return reg.DeleteProvider(ctx, "one") }},
		{"ListLanes", func() error { _, err := reg.ListLanes(ctx); return err }},
		{"ListPool", func() error { _, err := reg.ListPool(ctx, "p"); return err }},
		{"AdvancePoolIndex", func() error { _, err := reg.AdvancePoolIndex(ctx, "p"); return err }},
		{"Reader.GetProvider", func() error { _, err := reader.GetProvider(ctx, "one"); return err }},
		{"Reader.ListProviders", func() error { _, err := reader.ListProviders(ctx); return err }},
		{"Reader.GetByModel", func() error { _, err := reader.GetByModel(ctx, "claude-3-5-sonnet-20241022"); return err }},
		{"Reader.ListLanes", func() error { _, err := reader.ListLanes(ctx); return err }},
		{"Reader.ListPool", func() error { _, err := reader.ListPool(ctx, "p"); return err }},
	}
	for _, op := range ops {
		if err := op.call(); err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s on a closed handle must refuse as unavailable, got %v", op.name, err)
		}
	}
}

// TestTransactionWriteRefusalsSurface plants a SQLite RAISE(ABORT)
// trigger per case so the in-transaction write fails for real, proving
// each surface reports the failure and rolls back rather than half-
// completing.
func TestTransactionWriteRefusalsSurface(t *testing.T) {
	ctx := context.Background()

	laneDeleteRefused := newTestRegistry(t)
	upsertSeededProvider(t, laneDeleteRefused, "provoke")
	if err := laneDeleteRefused.UpsertLane(ctx, LaneRecord{
		LaneName: "provoke", ProviderName: "provoke",
		Capacity: CapacityAPICredit, State: LaneStateAvailable,
	}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	mustExecTrigger(t, laneDeleteRefused.db, `CREATE TRIGGER refuse_lane_delete BEFORE DELETE ON `+
		tableProviderLanes+` BEGIN SELECT RAISE(ABORT, 'lane delete refused'); END`)
	if err := laneDeleteRefused.DeleteProvider(ctx, "provoke"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a refused lane delete must surface, got %v", err)
	}
	if _, err := laneDeleteRefused.GetProvider(ctx, "provoke"); err != nil {
		t.Fatalf("the refused delete must roll back, not half-delete: %v", err)
	}

	providerDeleteRefused := newTestRegistry(t)
	upsertSeededProvider(t, providerDeleteRefused, "provoke")
	mustExecTrigger(t, providerDeleteRefused.db, `CREATE TRIGGER refuse_provider_delete BEFORE DELETE ON `+
		tableProviderRecords+` BEGIN SELECT RAISE(ABORT, 'provider delete refused'); END`)
	if err := providerDeleteRefused.DeleteProvider(ctx, "provoke"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a refused provider delete must surface, got %v", err)
	}

	poolUpdateRefused := newTestRegistry(t)
	upsertPoolMember(t, poolUpdateRefused, "provoke", "pool", LaneStateAvailable)
	mustExecTrigger(t, poolUpdateRefused.db, `CREATE TRIGGER refuse_lane_update BEFORE UPDATE ON `+
		tableProviderLanes+` BEGIN SELECT RAISE(ABORT, 'lane update refused'); END`)
	if _, err := poolUpdateRefused.AdvancePoolIndex(ctx, "pool"); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a refused pool-index update must surface, got %v", err)
	}
}
