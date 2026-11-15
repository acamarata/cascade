package capacity

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/providers/usage"
)

// TestQuotaWindowResolvesByProvider proves a provider named differently from
// the gateway account its windows are recorded under still shows them.
func TestQuotaWindowResolvesByProvider(t *testing.T) {
	ctx := context.Background()
	clk := &fakeClock{t: quotaNow}
	m := quotaStore(t, clk)
	if err := m.RecordQuota(ctx, usage.QuotaObservation{Provider: "acct", Window: topology.BucketWindow5h,
		Used: 10, Limit: 100, UtilizationPct: usage.QuotaUnknown, ObservedAt: quotaNow}); err != nil {
		t.Fatal(err)
	}
	src := &fakeProviderSource{
		providers: []registry.ProviderRecord{{Name: "prov"}},
		lanes:     []registry.LaneRecord{{ProviderName: "prov", Capacity: BucketInteractiveUsage, State: registry.LaneStateAvailable}},
	}
	slotFor := func() ProviderSlot {
		comp := NewCompositor(clk, time.Hour, "")
		comp.SetQuotaSource(usage.NewReader(m).QuotaWindows)
		if err := comp.UpdateProviders(ctx, src); err != nil {
			t.Fatal(err)
		}
		return comp.Snapshot().Providers["prov"]
	}
	if got := slotFor(); len(got.Quota) != 0 {
		t.Fatalf("unmapped provider shows %+v, want no windows", got.Quota)
	}
	t.Cleanup(SetDefaultQuotaKey(func(_ context.Context, p string) string {
		if p == "prov" {
			return "acct"
		}
		return ""
	}))
	slot := slotFor()
	if len(slot.Quota) != 1 || slot.Quota[0].Used != 10 {
		t.Fatalf("mapped provider quota = %+v, want the acct window", slot.Quota)
	}
}
