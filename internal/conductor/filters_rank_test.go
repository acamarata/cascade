package conductor

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// threeLocalLanes builds three healthy loopback lanes, lane-a..lane-c.
func threeLocalLanes() *fakeRegistry {
	reg := &fakeRegistry{}
	for _, n := range []string{"a", "b", "c"} {
		reg.providers = append(reg.providers, provider.ProviderInfo{Name: "prov-" + n, BaseURL: "http://127.0.0.1:9000", HealthStatus: "healthy"})
		reg.lanes = append(reg.lanes, provider.LaneInfo{LaneName: "lane-" + n, ProviderName: "prov-" + n, ModelFilter: []string{"model-" + n}})
	}
	return reg
}

// fixedRank ranks the given lanes in order (dropping any not offered)
// and records every candidate list it was handed.
type fixedRank struct {
	order []LaneID
	seen  [][]LaneID
	err   error
}

func (f *fixedRank) rank(_ context.Context, _ provider.ModelRequest, cands []LaneID) ([]LaneID, []string, error) {
	f.seen = append(f.seen, slices.Clone(cands))
	if f.err != nil {
		return nil, nil, f.err
	}
	var out []LaneID
	var notes []string
	for i, id := range f.order {
		if slices.Contains(cands, id) {
			out = append(out, id)
			notes = append(notes, []string{"0.9", "0.5", "0.1"}[i])
		}
	}
	return out, notes, nil
}

// rankedRouter builds a router over threeLocalLanes with quota and rank.
func rankedRouter(quota QuotaSpiller, rank RankFn, clock Clock) *DefaultRouter {
	r := NewRouter(threeLocalLanes(), quota, clock, nil)
	WithLaneTierCap(conductorTestCap)(r)
	WithRanker(rank)(r)
	return r
}

func TestRouterFinalPickIsRank(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	p := NewQuotaPolicy(QuotaConfig{CeilingOverrides: map[LaneID]int64{"lane-c": 1}}, clock)
	rk := &fixedRank{order: []LaneID{"lane-c", "lane-a", "lane-b"}}
	r := rankedRouter(p, rk.rank, clock)
	ctx := context.Background()

	sel, err := r.Select(ctx, internalReq())
	if err != nil || sel.LaneID != "lane-c" {
		t.Fatalf("first pick = %q, %v; want lane-c (Rank's first)", sel.LaneID, err)
	}
	if !containsPrefix(sel.ReasonFlags, "rank:lane-c:0.9") {
		t.Fatalf("flags = %v, want rank:lane-c:0.9", sel.ReasonFlags)
	}
	if want := []LaneID{"lane-a", "lane-b", "lane-c"}; !slices.Equal(rk.seen[0], want) {
		t.Fatalf("Rank saw %v, want the filtered lanes %v", rk.seen[0], want)
	}
	// lane-c is now at its ceiling: the next ranked lane is picked.
	sel, err = r.Select(ctx, internalReq())
	if err != nil || sel.LaneID != "lane-a" {
		t.Fatalf("second pick = %q, %v; want lane-a (next ranked)", sel.LaneID, err)
	}
	if !containsPrefix(sel.ReasonFlags, "rank:lane-a:0.5") {
		t.Fatalf("flags = %v, want rank:lane-a:0.5", sel.ReasonFlags)
	}

	// Without WithRanker the lane-name order is the ranking: no rank flag.
	plain := NewRouter(threeLocalLanes(), NewQuotaPolicy(QuotaConfig{}, clock), clock, nil)
	WithLaneTierCap(conductorTestCap)(plain)
	sel, err = plain.Select(ctx, internalReq())
	if err != nil || sel.LaneID != "lane-a" {
		t.Fatalf("default pick = %q, %v; want lane-a", sel.LaneID, err)
	}
	for _, f := range sel.ReasonFlags {
		if len(f) >= 5 && f[:5] == "rank:" {
			t.Fatalf("default router flags = %v, want no rank flag", sel.ReasonFlags)
		}
	}
}

func TestRouterSpillOrderPartitions(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	ctx := context.Background()
	rk := &fixedRank{order: []LaneID{"lane-a", "lane-b", "lane-c"}}
	cases := []struct {
		name  string
		spill []LaneID
		want  string
	}{
		{"configured-first-listed", []LaneID{"lane-b", "lane-a"}, "lane-b"},
		{"configured-skips-ineligible", []LaneID{"lane-z", "lane-c", "lane-a"}, "lane-c"},
		{"unconfigured-one-partition", nil, "lane-a"},
	}
	for _, tc := range cases {
		r := rankedRouter(NewQuotaPolicy(QuotaConfig{SpillOrder: tc.spill}, clock), rk.rank, clock)
		sel, err := r.Select(ctx, internalReq())
		if err != nil || sel.LaneID != tc.want {
			t.Fatalf("%s: pick = %q, %v; want %s", tc.name, sel.LaneID, err, tc.want)
		}
	}
	// Unconfigured, a different Rank order moves the pick with it.
	rev := &fixedRank{order: []LaneID{"lane-b", "lane-c", "lane-a"}}
	r := rankedRouter(NewQuotaPolicy(QuotaConfig{}, clock), rev.rank, clock)
	if sel, err := r.Select(ctx, internalReq()); err != nil || sel.LaneID != "lane-b" {
		t.Fatalf("unconfigured reversed: pick = %q, %v; want lane-b", sel.LaneID, err)
	}
}

// assertNoLane asserts err is ErrNoLane by identity and message.
func assertNoLane(t *testing.T, name string, err error) {
	t.Helper()
	if err != ErrNoLane || !strings.Contains(err.Error(), "conductor: no candidate lane") {
		t.Fatalf("%s: err = %v, want ErrNoLane", name, err)
	}
}

func TestRouterNilRankRefuses(t *testing.T) {
	ctx := context.Background()
	for name, opts := range map[string][]RouterOption{"absent": nil, "nil": {WithRanker(nil)}} {
		opts = append(opts, WithLaneTierCap(conductorTestCap))
		r, err := NewDaemonRouter(nil, nil, &fakeEventPublisher{}, nil, nil, opts...)
		if r != nil || err != ErrRankerMissing || !strings.Contains(err.Error(), "no RankFn supplied (WithRanker is required)") {
			t.Fatalf("%s: NewDaemonRouter = %v, %v; want ErrRankerMissing", name, r, err)
		}
		if k, _ := cascade.KindOf(err); k != cascade.KindInvalidInput {
			t.Fatalf("%s: kind = %v, want KindInvalidInput", name, k)
		}
	}
	good := &fixedRank{order: []LaneID{"lane-b"}}
	r, err := NewDaemonRouter(nil, nil, &fakeEventPublisher{}, nil, nil, WithLaneTierCap(conductorTestCap), WithRanker(good.rank))
	if err != nil || r == nil {
		t.Fatalf("NewDaemonRouter with WithRanker = %v, %v", r, err)
	}
	assertRankRefusals(ctx, t)
}

// assertRankRefusals covers the Select-time refusals: no RankFn, a
// failing RankFn and malformed rankings are all ErrNoLane, and a working
// RankFn over the same router selects.
func assertRankRefusals(ctx context.Context, t *testing.T) {
	t.Helper()
	quota := &fakeQuota{order: []LaneID{"lane-a", "lane-b", "lane-c"}}
	noRank := rankedRouter(quota, nil, nil)
	capReq := internalReq()
	capReq.RequiredCapabilities.Search = true
	for _, req := range []provider.ModelRequest{chatReq(), internalReq(), capReq} {
		_, err := noRank.Select(ctx, req)
		assertNoLane(t, "nil RankFn", err)
	}
	_, err := (&DefaultRouter{}).Select(ctx, chatReq())
	assertNoLane(t, "zero value", err)

	failing := &fixedRank{err: cascade.New(cascade.KindInternal, "rank store down")}
	d := &relayDispatch{}
	_, err = d.run(ctx, rankedRouter(quota, failing.rank, nil), internalReq())
	assertNoLane(t, "RankFn error", err)
	if d.local+d.remote != 0 || quota.callN != 0 {
		t.Fatalf("RankFn error dispatched local=%d remote=%d quota calls=%d; want 0", d.local, d.remote, quota.callN)
	}

	assertMalformedRankings(ctx, t, quota)
	good := &fixedRank{order: []LaneID{"lane-b"}}
	if sel, err := rankedRouter(quota, good.rank, nil).Select(ctx, internalReq()); err != nil || sel.LaneID != "lane-b" {
		t.Fatalf("working RankFn pick = %q, %v; want lane-b", sel.LaneID, err)
	}
}

// assertMalformedRankings asserts a ranking naming an unknown lane, a
// repeated lane, no lane, or a utility list of the wrong length is
// ErrNoLane rather than a pick.
func assertMalformedRankings(ctx context.Context, t *testing.T, quota QuotaSpiller) {
	t.Helper()
	bad := map[string]RankFn{
		"unknown lane": func(context.Context, provider.ModelRequest, []LaneID) ([]LaneID, []string, error) {
			return []LaneID{"lane-z"}, []string{"1"}, nil
		},
		"repeated lane": func(context.Context, provider.ModelRequest, []LaneID) ([]LaneID, []string, error) {
			return []LaneID{"lane-a", "lane-a"}, []string{"1", "1"}, nil
		},
		"empty": func(context.Context, provider.ModelRequest, []LaneID) ([]LaneID, []string, error) {
			return nil, nil, nil
		},
		"utility mismatch": func(context.Context, provider.ModelRequest, []LaneID) ([]LaneID, []string, error) {
			return []LaneID{"lane-a"}, nil, nil
		},
	}
	for name, fn := range bad {
		_, err := rankedRouter(quota, fn, nil).Select(ctx, internalReq())
		assertNoLane(t, name, err)
	}
}

func TestRelayOriginStillRestricts(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	rk := &fixedRank{order: []LaneID{"lane-c", "lane-b", "lane-a"}}
	r := rankedRouter(NewQuotaPolicy(QuotaConfig{}, clock), rk.rank, clock)

	if sel, err := r.Select(ctx, internalReq()); err != nil || sel.LaneID != "lane-c" {
		t.Fatalf("unrelayed pick = %q, %v; want lane-c (Rank's first)", sel.LaneID, err)
	}
	relayed := WithRelayOrigin(ctx, RelayOrigin{DispatchID: "d-1", LaneID: "lane-a", Model: "model-a"})
	sel, err := r.Select(relayed, internalReq())
	if err != nil || sel.LaneID != "lane-a" {
		t.Fatalf("relayed pick = %q, %v; want the record lane lane-a", sel.LaneID, err)
	}
	if last := rk.seen[len(rk.seen)-1]; !slices.Equal(last, []LaneID{"lane-a"}) {
		t.Fatalf("Rank saw %v for a relayed request, want only lane-a", last)
	}
	if !containsPrefix(sel.ReasonFlags, "rank:lane-a:0.1") {
		t.Fatalf("flags = %v, want rank:lane-a:0.1", sel.ReasonFlags)
	}
}
