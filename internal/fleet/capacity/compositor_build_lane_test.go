package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/provider"
	_ "modernc.org/sqlite"
)

func TestProviderSlotLaneFields(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{t: buildTestNow}
	reg := seedLaneRegistry(t, clk)
	comp := NewCompositor(clk, time.Hour, "")
	if err := comp.UpdateProviders(ctx, reg); err != nil {
		t.Fatal(err)
	}
	snap := comp.Snapshot()
	lanes, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes) != 6 || len(snap.Lanes) != len(lanes) {
		t.Fatalf("registry=%d snapshot=%d lanes", len(lanes), len(snap.Lanes))
	}
	for _, lane := range lanes {
		assertRegistryLane(t, reg, snap, lane)
	}
	assertProviderWireUnchanged(t, reg, snap)
	t.Run("unresolvable info", func(t *testing.T) {
		src := &fakeProviderSource{providers: []registry.ProviderRecord{{Name: "missing", BaseURL: "https://example.com", Tier: registry.TierStrong}},
			lanes: []registry.LaneRecord{{LaneName: "missing-lane", ProviderName: "missing", Capacity: BucketAPICredit, State: StateAvailable}}}
		if err := comp.UpdateProviders(ctx, src); err != nil {
			t.Fatal(err)
		}
		got := comp.Snapshot().Lanes
		if len(got) != 1 || got["missing-lane"].LaneID != "missing-lane" || got["missing-lane"].LaneType != conductor.LaneUnresolved {
			t.Fatalf("unresolvable provider: %#v", got)
		}
	})
}

func seedLaneRegistry(t *testing.T, clk *fakeClock) *registry.Registry {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatal(err)
	}
	reg := registry.NewRegistry(db, clk)
	for i, url := range []string{"http://127.0.0.1:9000", "https://example.com", ":bad"} {
		name := []string{"local", "remote", "unresolved"}[i]
		rec := registry.ProviderRecord{Name: name, Driver: registry.DriverOpenAICompat, BaseURL: url,
			Auth: registry.AuthKey, AuthRef: registry.VaultKeyRef("provider." + name), AccountKind: registry.AccountPersonal,
			Tier:         []registry.Tier{registry.TierStrong, registry.TierMid, registry.TierCheap}[i],
			Capabilities: provider.Capabilities{CompliancePosture: provider.NewCompliancePosture([]string{"api-key"}, false, true, []string{"batch"}, name, false)}}
		if err := reg.UpsertProvider(ctx, rec); err != nil {
			t.Fatal(err)
		}
		for j, suffix := range []string{"-a", "-b"} {
			lane := registry.LaneRecord{LaneName: name + suffix, ProviderName: name, Capacity: registry.CapacityAPICredit,
				State: []State{StateAvailable, StateConstrained}[j], ResetEstimate: clk.t.Add(time.Hour)}
			if err := reg.UpsertLane(ctx, lane); err != nil {
				t.Fatal(err)
			}
		}
	}
	return reg
}

func assertRegistryLane(t *testing.T, reg *registry.Registry, snap FleetSnapshot, lane registry.LaneRecord) {
	t.Helper()
	info, err := registry.NewReader(reg).GetProvider(context.Background(), lane.ProviderName)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := snap.Lanes[conductor.LaneID(lane.LaneName)]
	wantTier := map[string]Tier{"local": TierZero, "remote": TierOne, "unresolved": TierTwo}[lane.ProviderName]
	if !ok || got.LaneID != conductor.LaneID(lane.LaneName) || got.ProfileRef != lane.ProviderName ||
		got.Tier != wantTier || got.State != lane.State || got.LaneType != conductor.ClassifyLane(info) ||
		!reflect.DeepEqual(got.Posture, info.Capabilities.CompliancePosture) || !got.UpdatedAt.Equal(buildTestNow) {
		t.Fatalf("lane %q metadata: %#v; provider: %#v", lane.LaneName, got, info)
	}
}

func assertProviderWireUnchanged(t *testing.T, reg *registry.Registry, snap FleetSnapshot) {
	t.Helper()
	recs, err := reg.ListProviders(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 || len(snap.Providers) != len(recs) {
		t.Fatal("missing provider fixtures")
	}
	for _, rec := range recs {
		slot := snap.Providers[rec.Name]
		legacy := struct {
			ProfileRef     string                `json:"profile_ref"`
			Buckets        map[BucketKind]Bucket `json:"buckets"`
			State          State                 `json:"state"`
			ResetEstimate  time.Time             `json:"reset_estimate"`
			ReauthRequired bool                  `json:"reauth_required"`
			UpdatedAt      time.Time             `json:"updated_at"`
		}{slot.ProfileRef, slot.Buckets, slot.State, slot.ResetEstimate, slot.ReauthRequired, slot.UpdatedAt}
		got, err := json.Marshal(slot)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(legacy)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("provider JSON changed: %s != %s", got, want)
		}
	}
}
