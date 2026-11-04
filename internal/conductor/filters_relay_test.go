package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// twoHealthyLanes builds a registry with two healthy, controller-local
// lanes: lane-a serving model-a and lane-b serving model-b.
func twoHealthyLanes() *fakeRegistry {
	reg := oneHealthyLane()
	reg.providers = append(reg.providers, provider.ProviderInfo{
		Name:         "prov-b",
		BaseURL:      "http://127.0.0.1:8081",
		KnownModels:  []string{"model-b"},
		HealthStatus: "healthy",
	})
	reg.lanes = append(reg.lanes, provider.LaneInfo{
		LaneName:     "lane-b",
		ProviderName: "prov-b",
		State:        "active",
	})
	return reg
}

// assertRelayLaneUnavailable checks err is ErrRelayLaneUnavailable by
// identity, kind and message.
func assertRelayLaneUnavailable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrRelayLaneUnavailable) {
		t.Fatalf("err = %v, want ErrRelayLaneUnavailable", err)
	}
	if err.Error() != ErrRelayLaneUnavailable.Error() {
		t.Fatalf("message = %q, want %q", err.Error(), ErrRelayLaneUnavailable.Error())
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Fatalf("kind = %v (typed %t), want KindUnavailable", kind, ok)
	}
}

// TestRelayOriginRestrictsToRecordLane proves a relayed call can reach
// only the lane and model it was recorded against. The quota double
// prefers lane-b, so a router without the relay filter would pick lane-b
// and the first half fails.
func TestRelayOriginRestrictsToRecordLane(t *testing.T) {
	origin := RelayOrigin{DispatchID: "d-1", LaneID: "lane-a", Model: "model-a"}
	ctx := WithRelayOrigin(context.Background(), origin)

	quota := &fakeQuota{order: []LaneID{"lane-b", "lane-a"}}
	r := NewRouter(twoHealthyLanes(), quota, nil, nil)
	sel, flags, err := r.SelectExplain(ctx, chatReq())
	if err != nil {
		t.Fatalf("SelectExplain under relay origin: %v", err)
	}
	if sel.LaneID != "lane-a" || sel.Model != "model-a" {
		t.Fatalf("selection = %s/%s, want lane-a/model-a", sel.LaneID, sel.Model)
	}
	if !containsPrefix(flags, "relay:origin-lane=lane-a") {
		t.Fatalf("flags = %v, want the relay origin-lane flag", flags)
	}

	// Without the origin the same router picks the quota-preferred lane,
	// so the restriction above is the filter's doing.
	sel, err = NewRouter(twoHealthyLanes(), &fakeQuota{order: []LaneID{"lane-b", "lane-a"}}, nil, nil).
		Select(context.Background(), chatReq())
	if err != nil || sel.LaneID != "lane-b" {
		t.Fatalf("unrelayed selection = %q, %v; want lane-b", sel.LaneID, err)
	}

	// The origin lane unavailable: refused, and no other lane is tried.
	quota = &fakeQuota{order: []LaneID{"lane-b", "lane-a"}}
	r = NewRouter(twoHealthyLanes(), quota, nil, nil)
	sel, flags, err = r.SelectExplain(ctx, chatReq(), "lane-a")
	assertRelayLaneUnavailable(t, err)
	if sel.LaneID != "" {
		t.Fatalf("selection = %q, want none", sel.LaneID)
	}
	if quota.callN != 0 {
		t.Fatalf("quota consulted %d times, want 0: another lane was considered", quota.callN)
	}
	if !containsPrefix(flags, "relay:no-origin-lane") {
		t.Fatalf("flags = %v, want the no-origin-lane flag", flags)
	}

	// A model other than the origin model is refused.
	other := WithRelayOrigin(context.Background(), RelayOrigin{DispatchID: "d-2", LaneID: "lane-a", Model: "model-b"})
	quota = &fakeQuota{order: []LaneID{"lane-b", "lane-a"}}
	_, flags, err = NewRouter(twoHealthyLanes(), quota, nil, nil).SelectExplain(other, chatReq())
	assertRelayLaneUnavailable(t, err)
	if quota.callN != 0 || !containsPrefix(flags, "relay:model-mismatch") {
		t.Fatalf("quota calls = %d, flags = %v; want 0 and the model-mismatch flag", quota.callN, flags)
	}
}

// TestRelayOriginAbsentLeavesCandidatesUnchanged asserts the filter is
// inert when no relay origin is in scope.
func TestRelayOriginAbsentLeavesCandidatesUnchanged(t *testing.T) {
	snap, err := NewRouter(twoHealthyLanes(), nil, nil, nil).buildSnapshot(context.Background())
	if err != nil {
		t.Fatalf("buildSnapshot: %v", err)
	}
	out, flags, err := filterRelayOrigin(context.Background(), snap.lanes, nil)
	if err != nil || len(out) != 2 || len(flags) != 0 {
		t.Fatalf("got %d cands, flags %v, err %v; want 2, none, nil", len(out), flags, err)
	}
}
