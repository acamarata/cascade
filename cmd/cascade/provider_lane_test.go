package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/providers/intake"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

// Purpose (this file): the lane every registered provider must get.
//
// The W3 hardening gate found registry.UpsertLane with no production
// caller: the lane table was empty on every machine, so DefaultRouter's
// snapshot had nothing to select and every dispatch answered "no candidate
// lane". These assertions run against the REAL durable registry, because
// the defect was precisely that a seam had only test callers.
// SPORT: cmd/cascade tests (ADD) — P1-E10-W4-S87-T1.

// TestAddingAProviderCreatesItsLane is the regression.
func TestAddingAProviderCreatesItsLane(t *testing.T) {
	ctx := context.Background()
	reg := newTestProviderRegistry(t)

	rec := intake.ProviderRecord{Name: "compatsub", Driver: "anthropic", Auth: "key", AuthRef: "provider.compatsub.key"}
	if err := newRegistryAdapter(reg).UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 1 {
		t.Fatalf("lanes = %d, want exactly one for the added provider", len(lanes))
	}
	if lanes[0].ProviderName != "compatsub" || lanes[0].LaneName != "compatsub" {
		t.Errorf("lane = %+v, want it named for its provider", lanes[0])
	}
	if lanes[0].State != registry.LaneStateAvailable {
		t.Errorf("state = %q, want available after a live verify", lanes[0].State)
	}
	if len(lanes[0].ModelFilter) != 0 {
		t.Errorf("model filter = %v, want empty (all known models)", lanes[0].ModelFilter)
	}
}

// TestAnUnverifiedProviderGetsAnUnknownLane keeps the state honest: a
// provider added with --no-verify has proved nothing.
func TestAnUnverifiedProviderGetsAnUnknownLane(t *testing.T) {
	ctx := context.Background()
	reg := newTestProviderRegistry(t)

	rec := intake.ProviderRecord{Name: "unverified", Driver: "anthropic", Auth: "key",
		AuthRef: "provider.unverified.key", VerifySkipped: true}
	if err := newRegistryAdapter(reg).UpsertProvider(ctx, rec); err != nil {
		t.Fatal(err)
	}
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 1 || lanes[0].State != registry.LaneStateUnknown {
		t.Fatalf("lanes = %+v, want one lane in state unknown", lanes)
	}
}

// TestPooledProvidersDoNotCollideOnLaneName holds the primary-key hazard:
// lane_name is unique, so two pool members named without their pool would
// make the second add silently overwrite the first's lane.
func TestPooledProvidersDoNotCollideOnLaneName(t *testing.T) {
	ctx := context.Background()
	reg := newTestProviderRegistry(t)
	adapter := newRegistryAdapter(reg)

	for _, name := range []string{"key-a", "key-b"} {
		if err := adapter.UpsertProvider(ctx, intake.ProviderRecord{
			Name: name, Driver: "gemini", Auth: "key",
			AuthRef: intake.VaultKeyRef("provider." + name + ".key"), Pool: "free",
		}); err != nil {
			t.Fatal(err)
		}
	}
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 2 {
		t.Fatalf("lanes = %d (%+v), want one per pool member", len(lanes), lanes)
	}
	for _, l := range lanes {
		if l.PoolMembership != "free" {
			t.Errorf("lane %q has pool %q, want free", l.LaneName, l.PoolMembership)
		}
	}
}

// TestReAddingAProviderKeepsOneLane covers the documented re-add contract.
func TestReAddingAProviderKeepsOneLane(t *testing.T) {
	ctx := context.Background()
	reg := newTestProviderRegistry(t)
	adapter := newRegistryAdapter(reg)
	rec := intake.ProviderRecord{Name: "compatsub", Driver: "anthropic", Auth: "key", AuthRef: "provider.compatsub.key"}

	for i := 0; i < 3; i++ {
		if err := adapter.UpsertProvider(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 1 {
		t.Fatalf("lanes = %d, want a re-add to update the lane rather than add one", len(lanes))
	}
}

// newTestProviderRegistry opens a real, migrated providers registry on a
// temp-dir SQLite file. It is the REAL registry deliberately: the defect
// this file guards was a seam whose only callers were fakes.
func newTestProviderRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "providers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clock := runtime.NewSystemClock()
	if err := registry.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyMigrationSchema: %v", err)
	}
	return registry.NewRegistry(db, clock)
}
