package registry

// Purpose: the refusal surfaces a closed handle or malformed persisted
// data must produce: every read and write path fails with
// KindUnavailable (fail closed) instead of returning a partial or
// zero-value result, and a write refused mid-transaction rolls the whole
// transaction back so no orphan survives.

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// seedAndClose seeds one provider plus one pool lane, then closes the
// handle: every subsequent call must refuse.
func seedAndClose(t *testing.T) *Registry {
	t.Helper()
	reg := newTestRegistry(t)
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, sampleProvider("anthropic")); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if err := reg.UpsertLane(ctx, LaneRecord{
		LaneName: "anthropic", ProviderName: "anthropic", PoolMembership: "closed-pool",
		Capacity: CapacityAPICredit, State: LaneStateAvailable,
	}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	if err := reg.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return reg
}

// TestClosedHandleSurfacesUnavailableRefusals: a closed handle makes every
// surface refuse with KindUnavailable, never a silent empty result.
func TestClosedHandleSurfacesUnavailableRefusals(t *testing.T) {
	reg := seedAndClose(t)
	ctx := context.Background()
	calls := []struct {
		name string
		call func() error
	}{
		{"GetProvider", func() error { _, err := reg.GetProvider(ctx, "anthropic"); return err }},
		{"ListProviders", func() error { _, err := reg.ListProviders(ctx); return err }},
		{"GetByModel", func() error { _, err := reg.GetByModel(ctx, "claude-3-5-sonnet-20241022"); return err }},
		{"ListLanes", func() error { _, err := reg.ListLanes(ctx); return err }},
		{"ListPool", func() error { _, err := reg.ListPool(ctx, "closed-pool"); return err }},
		{"UpsertProvider", func() error { return reg.UpsertProvider(ctx, sampleProvider("fresh")) }},
		{"UpsertLane", func() error {
			return reg.UpsertLane(ctx, LaneRecord{LaneName: "fresh", ProviderName: "fresh",
				Capacity: CapacityAPICredit, State: LaneStateAvailable})
		}},
		{"DeleteProvider", func() error { return reg.DeleteProvider(ctx, "anthropic") }},
		{"AdvancePoolIndex", func() error { _, err := reg.AdvancePoolIndex(ctx, "closed-pool"); return err }},
		{"AtomicHealthUpdate", func() error {
			_, err := reg.AtomicHealthUpdate(ctx, "anthropic",
				func(rec ProviderRecord) (ProviderRecord, error) { return rec, nil })
			return err
		}},
	}
	for _, c := range calls {
		if err := c.call(); err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("%s on a closed handle: got %v, want a KindUnavailable refusal", c.name, err)
		}
	}
}

// TestReaderPropagatesUnavailableFromClosedHandle: the pkg/-level Reader
// surfaces the same refusal; it never masks one with an empty listing.
func TestReaderPropagatesUnavailableFromClosedHandle(t *testing.T) {
	reader := NewReader(seedAndClose(t))
	ctx := context.Background()
	calls := []struct {
		name string
		call func() error
	}{
		{"GetProvider", func() error { _, err := reader.GetProvider(ctx, "anthropic"); return err }},
		{"ListProviders", func() error { _, err := reader.ListProviders(ctx); return err }},
		{"GetByModel", func() error { _, err := reader.GetByModel(ctx, "claude-3-5-sonnet-20241022"); return err }},
		{"ListLanes", func() error { _, err := reader.ListLanes(ctx); return err }},
		{"ListPool", func() error { _, err := reader.ListPool(ctx, "closed-pool"); return err }},
	}
	for _, c := range calls {
		if err := c.call(); err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Errorf("Reader.%s on a closed handle: got %v, want a KindUnavailable refusal", c.name, err)
		}
	}
}

// TestMalformedLaneRecordRefusesListings: a persisted model_filter that is
// not valid JSON makes both lane listings refuse rather than return a
// half-parsed lane.
func TestMalformedLaneRecordRefusesListings(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertPoolMember(t, reg, "anthropic", "gf-pool", LaneStateAvailable)
	if _, err := reg.db.ExecContext(ctx,
		`UPDATE `+tableProviderLanes+` SET model_filter = ? WHERE lane_name = ?`,
		"{not-valid-json", "anthropic"); err != nil {
		t.Fatalf("corrupt model_filter: %v", err)
	}
	if _, err := reg.ListLanes(ctx); err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("ListLanes on a malformed lane: got %v, want KindUnavailable", err)
	}
	if _, err := reg.ListPool(ctx, "gf-pool"); err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("ListPool on a malformed lane: got %v, want KindUnavailable", err)
	}
}

// TestMalformedProviderRecordRefusesListProviders: one malformed row makes
// the whole listing refuse; a partial listing would silently hide the
// corrupt provider from every routing decision built on it.
func TestMalformedProviderRecordRefusesListProviders(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	for _, name := range []string{"alpha", "beta"} {
		if err := reg.UpsertProvider(ctx, sampleProvider(name)); err != nil {
			t.Fatalf("UpsertProvider(%s): %v", name, err)
		}
	}
	if _, err := reg.db.ExecContext(ctx,
		`UPDATE `+tableProviderRecords+` SET known_models = ? WHERE name = ?`,
		"{not-valid-json", "alpha"); err != nil {
		t.Fatalf("corrupt known_models: %v", err)
	}
	if _, err := reg.ListProviders(ctx); err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("ListProviders on a malformed row: got %v, want KindUnavailable", err)
	}
}

// refusedDeleteFixture seeds one provider with one lane and installs the
// refusing trigger; shared by the two mid-transaction rollback tests.
func refusedDeleteFixture(t *testing.T, trigger string) *Registry {
	t.Helper()
	reg := newTestRegistry(t)
	ctx := context.Background()
	if err := reg.UpsertProvider(ctx, sampleProvider("anthropic")); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if err := reg.UpsertLane(ctx, LaneRecord{LaneName: "anthropic", ProviderName: "anthropic",
		Capacity: CapacityAPICredit, State: LaneStateAvailable}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	if _, err := reg.db.ExecContext(ctx, trigger); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	return reg
}

// assertDeleteRolledBack: after a refused DeleteProvider both sides of the
// cascade must still be present.
func assertDeleteRolledBack(t *testing.T, reg *Registry) {
	t.Helper()
	if _, err := reg.GetProvider(context.Background(), "anthropic"); err != nil {
		t.Fatalf("the refused delete rolled back, but the provider is gone: %v", err)
	}
	lanes, err := reg.ListLanes(context.Background())
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	if len(lanes) != 1 {
		t.Fatalf("after rollback ListLanes = %d lanes, want the original 1 (no orphan)", len(lanes))
	}
}

// TestDeleteProviderRefusedAtLaneDeleteRollsBack: when the lane delete
// refuses, the provider row must survive.
func TestDeleteProviderRefusedAtLaneDeleteRollsBack(t *testing.T) {
	reg := refusedDeleteFixture(t, `CREATE TRIGGER no_lane_delete BEFORE DELETE ON `+tableProviderLanes+
		` BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	err := reg.DeleteProvider(context.Background(), "anthropic")
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("DeleteProvider under a refusing lane trigger: got %v, want KindUnavailable", err)
	}
	assertDeleteRolledBack(t, reg)
}

// TestDeleteProviderRefusedAtRecordDeleteRollsBack: when the record delete
// refuses after the lanes were already deleted inside the transaction, the
// lane deletes must roll back with it, leaving no orphan lanes.
func TestDeleteProviderRefusedAtRecordDeleteRollsBack(t *testing.T) {
	reg := refusedDeleteFixture(t, `CREATE TRIGGER no_record_delete BEFORE DELETE ON `+tableProviderRecords+
		` BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	err := reg.DeleteProvider(context.Background(), "anthropic")
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("DeleteProvider under a refusing record trigger: got %v, want KindUnavailable", err)
	}
	assertDeleteRolledBack(t, reg)
}

// TestAdvancePoolIndexWriteRefusedRollsBack: a refused pool_index UPDATE
// surfaces KindUnavailable (not the typed exhaustion refusal) and leaves
// the index untouched.
func TestAdvancePoolIndexWriteRefusedRollsBack(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertPoolMember(t, reg, "gf-alpha", "gf-pool", LaneStateAvailable)
	if _, err := reg.db.ExecContext(ctx, `CREATE TRIGGER no_pool_update BEFORE UPDATE ON `+
		tableProviderLanes+` BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	_, err := reg.AdvancePoolIndex(ctx, "gf-pool")
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("AdvancePoolIndex under a refusing update: got %v, want KindUnavailable", err)
	}
	members, err := reg.ListPool(ctx, "gf-pool")
	if err != nil {
		t.Fatalf("ListPool: %v", err)
	}
	if len(members) != 1 || members[0].PoolIndex != 0 {
		t.Fatalf("after rollback members = %+v, want the untouched pool_index 0", members)
	}
}

// TestAdvancePoolIndexSelectFailureIsNotExhaustion: a failing read (here,
// a dropped table) surfaces KindUnavailable, never the typed exhaustion
// refusal, which is reserved for a pool with no available member.
func TestAdvancePoolIndexSelectFailureIsNotExhaustion(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertPoolMember(t, reg, "gf-alpha", "gf-pool", LaneStateAvailable)
	if _, err := reg.db.ExecContext(ctx, `DROP TABLE `+tableProviderLanes); err != nil {
		t.Fatalf("drop lanes table: %v", err)
	}
	_, err := reg.AdvancePoolIndex(ctx, "gf-pool")
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("AdvancePoolIndex with a missing lanes table: got %v, want KindUnavailable", err)
	}
	if cascade.HasKind(err, cascade.KindQuotaExhausted) || isPoolExhausted(err) {
		t.Fatalf("a read failure must not masquerade as pool exhaustion: %v", err)
	}
}

// TestAdvancePoolIndexMalformedPoolIndexRefuses: text persisted into the
// integer pool_index column makes the MAX read unscannable; the dispatch
// must refuse rather than guess an index.
func TestAdvancePoolIndexMalformedPoolIndexRefuses(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertPoolMember(t, reg, "gf-alpha", "gf-pool", LaneStateAvailable)
	upsertPoolMember(t, reg, "gf-beta", "gf-pool", LaneStateAvailable)
	if _, err := reg.db.ExecContext(ctx,
		`UPDATE `+tableProviderLanes+` SET pool_index = ? WHERE lane_name = ?`,
		"not-a-number", "gf-beta"); err != nil {
		t.Fatalf("corrupt pool_index: %v", err)
	}
	_, err := reg.AdvancePoolIndex(ctx, "gf-pool")
	if err == nil || !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("AdvancePoolIndex with a malformed pool_index: got %v, want KindUnavailable", err)
	}
}
