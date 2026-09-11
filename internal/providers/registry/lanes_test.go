package registry

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func upsertOwningProvider(t *testing.T, reg *Registry, name string) {
	t.Helper()
	if err := reg.UpsertProvider(context.Background(), sampleProvider(name)); err != nil {
		t.Fatalf("UpsertProvider(%s): %v", name, err)
	}
}

func TestUpsertLaneValidates(t *testing.T) {
	reg := newTestRegistry(t)
	upsertOwningProvider(t, reg, "anthropic")
	bad := LaneRecord{LaneName: "", ProviderName: "anthropic", Capacity: CapacityInteractiveUsage, State: LaneStateAvailable}
	if err := reg.UpsertLane(context.Background(), bad); err == nil {
		t.Fatal("UpsertLane with an empty lane_name should have failed")
	}
}

// TestListLanesStableOrder is the acceptance criterion's golden fixture:
// >=5 lanes with varied names come back in stable lexicographic order.
func TestListLanesStableOrder(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertOwningProvider(t, reg, "anthropic")

	names := []string{"zeta-lane", "alpha-lane", "mu-lane", "beta-lane", "omega-lane"}
	for _, n := range names {
		if err := reg.UpsertLane(ctx, LaneRecord{
			LaneName: n, ProviderName: "anthropic",
			Capacity: CapacityInteractiveUsage, State: LaneStateAvailable, Weight: 1,
		}); err != nil {
			t.Fatalf("UpsertLane(%s): %v", n, err)
		}
	}

	got, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	gotNames := make([]string, len(got))
	for i, l := range got {
		gotNames[i] = l.LaneName
	}
	want := []string{"alpha-lane", "beta-lane", "mu-lane", "omega-lane", "zeta-lane"}
	if len(gotNames) != len(want) {
		t.Fatalf("ListLanes returned %d lanes, want %d", len(gotNames), len(want))
	}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Fatalf("ListLanes order = %v, want %v", gotNames, want)
		}
	}
	assertGolden(t, "listlanes_stable.golden.json", gotNames)
}

// TestLaneRecordCapacityStateGolden golden-tests every R-16.10 LaneState
// value round-tripping through UpsertLane/ListLanes.
func TestLaneRecordCapacityStateGolden(t *testing.T) {
	reg := newTestRegistry(t)
	ctx := context.Background()
	upsertOwningProvider(t, reg, "anthropic")

	states := []LaneState{LaneStateAvailable, LaneStateConstrained, LaneStateExhausted, LaneStateAuthRequired, LaneStateUnknown}
	resetAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for i, st := range states {
		lane := LaneRecord{
			LaneName: laneNameForState(st), ProviderName: "anthropic",
			Capacity: CapacityAgentSDKCredit, State: st, Weight: i + 1,
		}
		if st != LaneStateAvailable {
			lane.ResetEstimate = resetAt
		}
		if err := reg.UpsertLane(ctx, lane); err != nil {
			t.Fatalf("UpsertLane(%s): %v", st, err)
		}
	}

	got, err := reg.ListLanes(ctx)
	if err != nil {
		t.Fatalf("ListLanes: %v", err)
	}
	type row struct {
		LaneName      string `json:"lane_name"`
		Capacity      string `json:"capacity"`
		State         string `json:"state"`
		ResetEstimate string `json:"reset_estimate,omitempty"`
	}
	rows := make([]row, len(got))
	for i, l := range got {
		r := row{LaneName: l.LaneName, Capacity: string(l.Capacity), State: string(l.State)}
		if !l.ResetEstimate.IsZero() {
			r.ResetEstimate = l.ResetEstimate.Format(time.RFC3339)
		}
		rows[i] = r
	}
	assertGolden(t, "capacity_state.golden.json", rows)
}

func laneNameForState(s LaneState) string {
	return "lane-" + string(s)
}

// TestReaderAdaptsRegistryToPkgProviderReader proves *Reader satisfies
// provider.ProviderRegistryReader end to end over a real *Registry, and
// that every conversion (ProviderRecord->ProviderInfo,
// LaneRecord->LaneInfo) round-trips the fields a router needs.
const readerTestPool = "gf-pool"

// newReaderTestFixture seeds one provider with one pool-joined lane and
// returns the pkg/provider.ProviderRegistryReader adapter over it, split
// out of the assertion functions below to keep each under the 50-line cap.
func newReaderTestFixture(t *testing.T) (provider.ProviderRegistryReader, ProviderRecord) {
	t.Helper()
	reg := newTestRegistry(t)
	ctx := context.Background()
	rec := sampleProvider("anthropic")
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if err := reg.UpsertLane(ctx, LaneRecord{
		LaneName: "anthropic", ProviderName: "anthropic", PoolMembership: readerTestPool,
		Capacity: CapacityAPICredit, State: LaneStateAvailable, Weight: 2,
	}); err != nil {
		t.Fatalf("UpsertLane: %v", err)
	}
	return NewReader(reg), rec
}

func TestReaderAdaptsProviderMethods(t *testing.T) {
	reader, rec := newReaderTestFixture(t)
	ctx := context.Background()

	gotProvider, err := reader.GetProvider(ctx, "anthropic")
	if err != nil {
		t.Fatalf("Reader.GetProvider: %v", err)
	}
	if gotProvider.Name != "anthropic" || gotProvider.Driver != string(DriverAnthropic) || gotProvider.Tier != string(TierStrong) {
		t.Fatalf("Reader.GetProvider = %+v", gotProvider)
	}

	allProviders, err := reader.ListProviders(ctx)
	if err != nil {
		t.Fatalf("Reader.ListProviders: %v", err)
	}
	if len(allProviders) != 1 {
		t.Fatalf("Reader.ListProviders = %+v, want 1", allProviders)
	}

	byModel, err := reader.GetByModel(ctx, rec.KnownModels[0])
	if err != nil {
		t.Fatalf("Reader.GetByModel: %v", err)
	}
	if len(byModel) != 1 || byModel[0].Name != "anthropic" {
		t.Fatalf("Reader.GetByModel = %+v", byModel)
	}
}

func TestReaderAdaptsLaneMethods(t *testing.T) {
	reader, _ := newReaderTestFixture(t)
	ctx := context.Background()

	allLanes, err := reader.ListLanes(ctx)
	if err != nil {
		t.Fatalf("Reader.ListLanes: %v", err)
	}
	if len(allLanes) != 1 || allLanes[0].LaneName != "anthropic" || allLanes[0].Weight != 2 {
		t.Fatalf("Reader.ListLanes = %+v", allLanes)
	}

	poolLanes, err := reader.ListPool(ctx, readerTestPool)
	if err != nil {
		t.Fatalf("Reader.ListPool: %v", err)
	}
	if len(poolLanes) != 1 || poolLanes[0].State != string(LaneStateAvailable) {
		t.Fatalf("Reader.ListPool = %+v", poolLanes)
	}
}

// TestReaderPropagatesUnderlyingErrors proves the adapter does not
// swallow errors from the wrapped *Registry.
func TestReaderPropagatesUnderlyingErrors(t *testing.T) {
	reg := newTestRegistry(t)
	reader := NewReader(reg)
	if _, err := reader.GetProvider(context.Background(), "missing"); err == nil {
		t.Fatal("Reader.GetProvider for a missing name should propagate the underlying not-found error")
	}
}
