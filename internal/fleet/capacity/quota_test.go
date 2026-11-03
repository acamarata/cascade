package capacity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
	"github.com/acamarata/cascade/internal/storage/migrate"
)

var quotaNow = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func gatewayProviderSource() *fakeProviderSource {
	return &fakeProviderSource{
		providers: []registry.ProviderRecord{{Name: "gw"}},
		lanes:     []registry.LaneRecord{{ProviderName: "gw", Capacity: BucketInteractiveUsage, State: registry.LaneStateAvailable}},
	}
}

func quotaStore(t *testing.T, clk *fakeClock) *usage.Manager {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "provider-usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := usage.ApplyMigrationSchema(context.Background(), db, migrate.SQLiteEmitter{}, clk, "", ""); err != nil {
		t.Fatal(err)
	}
	return usage.NewManager(db, clk)
}

func composeWith(t *testing.T, clk *fakeClock, q QuotaSource) ProviderSlot {
	t.Helper()
	comp := NewCompositor(clk, time.Hour, "")
	comp.SetQuotaSource(q)
	if err := comp.UpdateProviders(context.Background(), gatewayProviderSource()); err != nil {
		t.Fatal(err)
	}
	return comp.Snapshot().Providers["gw"]
}

func staticQuota(obs ...usage.QuotaObservation) QuotaSource {
	return func(context.Context, string) ([]usage.QuotaObservation, error) { return obs, nil }
}

func TestQuotaStaleNeverZero(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{t: quotaNow}
	m := quotaStore(t, clk)
	r := usage.NewReader(m)
	if err := m.RecordQuota(ctx, usage.QuotaObservation{Provider: "gw", Window: topology.BucketWindow5h,
		Used: 10, Limit: 100, UtilizationPct: usage.QuotaUnknown, ObservedAt: quotaNow}); err != nil {
		t.Fatal(err)
	}
	if err := m.RecordQuotaFailure(ctx, "gw", topology.BucketWindow5h, "unavailable"); err != nil {
		t.Fatal(err)
	}
	obs, err := r.QuotaWindows(ctx, "gw")
	if err != nil || len(obs) != 1 || !obs[0].Stale || obs[0].LastError != "unavailable" || obs[0].Used != 10 {
		t.Fatalf("failed read = %+v, %v; want last values, Stale, content-free class", obs, err)
	}
	slot := composeWith(t, clk, r.QuotaWindows)
	if got := slot.Buckets[BucketInteractiveUsage].FiveHour.UtilizationPct; got != WindowUtilizationUnknown {
		t.Fatalf("stale five_hour = %v, want WindowUtilizationUnknown", got)
	}
	if len(slot.Quota) != 1 || !slot.Quota[0].Stale || slot.Quota[0].UtilizationPct != 10 || !slot.Quota[0].ObservedAt.Equal(quotaNow) {
		t.Fatalf("fleet quota = %+v, want stale with the last values", slot.Quota)
	}
	row := Compose(FleetSnapshot{Providers: map[string]ProviderSlot{"gw": slot}}, 0, nil, quotaNow, 1).Rows[0]
	if row.FiveHour.UtilizationPct != nil {
		t.Fatalf("widget five_hour = %v, want null", *row.FiveHour.UtilizationPct)
	}
	noLimit := usage.QuotaObservation{Window: topology.BucketWindow5h, Used: 5, Limit: 0, UtilizationPct: usage.QuotaUnknown, ObservedAt: quotaNow}
	if u := noLimit.Utilization(); u != -1 {
		t.Fatalf("Limit=0 utilization = %v, want -1", u)
	}
	slot = composeWith(t, clk, staticQuota(noLimit))
	if got := slot.Buckets[BucketInteractiveUsage].FiveHour.UtilizationPct; got != WindowUtilizationUnknown || slot.Quota[0].UtilizationPct != -1 {
		t.Fatalf("Limit=0 maps to %v / %v, want unknown", got, slot.Quota[0].UtilizationPct)
	}
	failing := func(context.Context, string) ([]usage.QuotaObservation, error) { return nil, errors.New("down") }
	slot = composeWith(t, clk, failing)
	if got := slot.Buckets[BucketInteractiveUsage].FiveHour.UtilizationPct; got != WindowUtilizationUnknown {
		t.Fatalf("failed source maps to %v, want unknown", got)
	}
}

func TestCompositorMapsQuotaWindows(t *testing.T) {
	clk := &fakeClock{t: quotaNow}
	obs := func(w topology.BucketWindow, u float64) usage.QuotaObservation {
		return usage.QuotaObservation{Provider: "gw", Window: w, Used: -1, Limit: -1, UtilizationPct: u, ObservedAt: quotaNow,
			ResetsAt: quotaNow.Add(2 * time.Hour)}
	}
	slot := composeWith(t, clk, staticQuota(obs(topology.BucketWindow5h, 150), obs(topology.BucketWindowWeek, 30),
		obs(topology.BucketWindowDay, 7), obs(topology.BucketWindowMonth, 9)))
	b := slot.Buckets[BucketInteractiveUsage]
	if b.FiveHour.UtilizationPct != 100 || b.SevenDay.UtilizationPct != 30 {
		t.Fatalf("five_hour=%v seven_day=%v, want 100 (clamped) and 30", b.FiveHour.UtilizationPct, b.SevenDay.UtilizationPct)
	}
	if b.FiveHour.ResetsIn != 2*time.Hour {
		t.Fatalf("five_hour resets_in = %v, want 2h from ResetsAt", b.FiveHour.ResetsIn)
	}
	if len(slot.Quota) != 4 {
		t.Fatalf("quota list = %+v, want all four windows", slot.Quota)
	}
	raw, err := json.Marshal(slot)
	if err != nil || !strings.Contains(string(raw), `"quota":[`) || !strings.Contains(string(raw), `"window":"day"`) {
		t.Fatalf("fleet.capacity slot JSON = %s, %v", raw, err)
	}
	day := composeWith(t, clk, staticQuota(obs(topology.BucketWindowDay, 7)))
	if got := day.Buckets[BucketInteractiveUsage]; got.FiveHour.UtilizationPct != WindowUtilizationUnknown || got.SevenDay.UtilizationPct != WindowUtilizationUnknown {
		t.Fatalf("day window leaked into the widget windows: %+v", got)
	}
}

func TestWidgetRowShowsGatewayUtilization(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{t: quotaNow}
	m := quotaStore(t, clk)
	if err := m.RecordQuota(ctx, usage.QuotaObservation{Provider: "gw", Window: topology.BucketWindow5h,
		Used: 420, Limit: 1000, UtilizationPct: 42.5, ObservedAt: quotaNow}); err != nil {
		t.Fatal(err)
	}
	comp := NewCompositor(clk, time.Hour, "")
	comp.SetQuotaSource(usage.NewReader(m).QuotaWindows)
	if err := comp.UpdateProviders(ctx, gatewayProviderSource()); err != nil {
		t.Fatal(err)
	}
	row := Compose(comp.Snapshot(), 0, nil, clk.Now(), 1).Rows[0]
	if row.FiveHour.UtilizationPct == nil || *row.FiveHour.UtilizationPct != 42.5 {
		t.Fatalf("five_hour.utilization_pct = %v, want 42.5", row.FiveHour.UtilizationPct)
	}
	clk.Advance(usage.QuotaStaleAfter + time.Second)
	if err := comp.UpdateProviders(ctx, gatewayProviderSource()); err != nil {
		t.Fatal(err)
	}
	row = Compose(comp.Snapshot(), 0, nil, clk.Now(), 2).Rows[0]
	if row.FiveHour.UtilizationPct != nil {
		t.Fatalf("stale five_hour.utilization_pct = %v, want null", *row.FiveHour.UtilizationPct)
	}
}
