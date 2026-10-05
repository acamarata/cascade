package economics

import (
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

var allKinds = []topology.QuotaDomainKind{
	topology.QuotaDomainAPIProject, topology.QuotaDomainSubscriptionWindow, topology.QuotaDomainSharedPool,
}

var allDimensions = []string{
	topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD,
	topology.DimensionSession5h, topology.DimensionWeeklyShared, topology.DimensionWeeklyModelFraction,
	topology.DimensionMonthly, topology.DimensionWindow5h, topology.DimensionWeekly,
}

// TestReservationUnitsTable enumerates every closed (kind, dimension) of
// topology and fails on a missing row, a row outside topology's sets, or
// a unit that does not match the published estimate -> dimension map.
func TestReservationUnitsTable(t *testing.T) {
	e := Estimate{TokensIn: 7, TokensOut: 11, Requests: 3}
	rows := 0
	for _, kind := range allKinds {
		for _, dim := range allDimensions {
			fn, ok := ReservationUnits[kind][dim]
			if topology.ValidDimensionName(kind, dim) != ok {
				t.Errorf("ReservationUnits[%s][%s] present=%v, want %v (topology's closed set)", kind, dim, ok, !ok)
				continue
			}
			if !ok {
				continue
			}
			rows++
			want := e.Requests
			if kind == topology.QuotaDomainAPIProject && dim == topology.DimensionTPM {
				want = e.TokensIn + e.TokensOut
			}
			if got := fn(e); got != want {
				t.Errorf("ReservationUnits[%s][%s](%+v) = %d, want %d", kind, dim, e, got, want)
			}
		}
	}
	if len(ReservationUnits) != len(allKinds) || rows != 10 {
		t.Errorf("ReservationUnits has %d kinds and %d rows, want 3 kinds and 10 rows", len(ReservationUnits), rows)
	}
}

// TestLeaseCapacityFromRealTopologyBuckets: an api_project domain whose
// rpm/tpm/rpd buckets were written by QuotaStore.UpsertBucket admits
// with a non-zero available figure per dimension, and UpsertBucket
// refuses a bucket named tokens_in for that domain.
func TestLeaseCapacityFromRealTopologyBuckets(t *testing.T) {
	tf := newTopoFixture(t)
	tf.seedDomain(t, "acct-1", topology.AccountRoleWorkforce, "domain-1", topology.QuotaDomainAPIProject)
	scope := topology.AccountScope("acct-1")
	caps := map[string]int64{topology.DimensionRPM: 60, topology.DimensionTPM: 100000, topology.DimensionRPD: 1000}
	for dim, c := range caps {
		tf.bucket(t, "domain-1", dim, c, c, scope)
	}
	bad := topology.Bucket{Name: "tokens_in", Limit: 10, Window: topology.BucketWindowDay, Source: topology.SourceProviderStatus, LimitScopeID: scope, CapacityObserved: 10}
	requireSentinel(t, tf.quotas.UpsertBucket(t.Context(), "domain-1", "tokens_in", bad), topology.ErrTopologyInvariant, "tokens_in")

	f := newReserverFixture(t, 0)
	f.seams.Buckets = tf.bucketsSeam()
	f.seams.ReserveFraction = tf.reserveFractionSeam()
	rv := f.reserver(t)
	req := baseReserveRequest()
	r, err := rv.Reserve(t.Context(), req)
	if err != nil || r.State != ReservationHeld || r.ScopeID != string(scope) {
		t.Fatalf("Reserve = %+v, %v, want held on %s", r, err, scope)
	}
	kind, gotScope, buckets, err := tf.bucketsSeam()(t.Context(), "domain-1")
	if err != nil {
		t.Fatalf("buckets: %v", err)
	}
	others, err := f.store.activeOnScope(t.Context(), string(gotScope), "")
	if err != nil {
		t.Fatalf("activeOnScope: %v", err)
	}
	avail, err := dimensionAvailability(kind, gotScope, buckets, others, f.clock.Now())
	if err != nil {
		t.Fatalf("dimensionAvailability: %v", err)
	}
	want := map[string]int64{
		topology.DimensionRPM: 60 - req.Estimate.Requests,
		topology.DimensionTPM: 100000 - req.Estimate.TokensIn - req.Estimate.TokensOut,
		topology.DimensionRPD: 1000 - req.Estimate.Requests,
	}
	for dim, w := range want {
		if avail[dim] != w || w <= 0 {
			t.Errorf("available[%s] = %d, want %d (non-zero, net of the held row)", dim, avail[dim], w)
		}
	}
}
