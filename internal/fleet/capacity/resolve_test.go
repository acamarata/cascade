// Purpose: the read-only half of Lease: allowed lanes from the snapshot, the
// agent-only restriction, the pluggable floors running after the sensitivity
// filter, the quota-domain refusal, the dispatch reference and the package's
// import boundary.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestFleetLeaseAllowedLanesFromSnapshot(t *testing.T) {
	snap := testSnapshot(newFx(t).clock.t)
	un := snap.Lanes["lane-t2"]
	un.Posture.ProgrammaticEntitlement = false
	snap.Lanes["lane-t2"] = un
	snap.Lanes["agent:codex"] = ProviderSlot{LaneID: "agent:codex", Posture: entitled(), Tier: TierZero}
	if got := allowedLanes(snap, false); !slices.Equal(got, []conductor.LaneID{"lane-t0", "lane-t1"}) {
		t.Fatalf("allowedLanes(non-agent) = %v, want [lane-t0 lane-t1]", got)
	}
	if got := allowedLanes(snap, true); !slices.Equal(got, []conductor.LaneID{"agent:codex"}) {
		t.Fatalf("allowedLanes(agent-only) = %v, want [agent:codex]", got)
	}
	if got := allowedLanes(FleetSnapshot{}, false); got == nil || len(got) != 0 {
		t.Fatalf("allowedLanes(empty) = %#v, want a non-nil empty slice", got)
	}

	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	f.snap = snap
	l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	if err != nil || l.LaneID != "lane-t1" {
		t.Fatalf("Lease = (%+v, %v), want lane-t1: the unentitled tier-2 lane is never selected", l, err)
	}

	empty := newFx(t)
	empty.seedJob("job-1", provider.SensitivityInternal)
	empty.snap = FleetSnapshot{}
	_, err = empty.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoAllowedLanes)
	empty.zeroRows()
}

func TestFleetLeaseAgentOnlySelectsAgentLane(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	d := dispatchFor("job-1")
	d.AgentOnly = true
	_, err := f.fleet.Lease(t.Context(), request(), d, 1)
	requireSentinel(t, err, ErrNoAllowedLanes)
	f.zeroRows()

	mk := func(id string, lt conductor.LaneType) ProviderSlot {
		return ProviderSlot{LaneID: conductor.LaneID(id), Posture: entitled(), Tier: TierZero, LaneType: lt, ProfileRef: id, State: StateAvailable, UpdatedAt: f.clock.t}
	}
	f.snap.Lanes["agent:alpha"] = mk("agent:alpha", conductor.LaneUnresolved)
	f.snap.Lanes["agent:beta"] = mk("agent:beta", conductor.LaneControllerLocal)
	f.snap.Lanes["agent:gamma"] = mk("agent:gamma", conductor.LaneControllerLocal)
	l, err := f.fleet.Lease(t.Context(), request(), d, 1)
	if err != nil || l.LaneID != "agent:beta" {
		t.Fatalf("Lease(AgentOnly) = (%+v, %v), want agent:beta: first entitled agent lane the sensitivity table admits", l, err)
	}
	other, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	if err != nil || strings.HasPrefix(string(other.LaneID), "agent:") {
		t.Fatalf("Lease(not AgentOnly) = (%+v, %v), want a non-agent lane", other, err)
	}
}

// floorStub records every slot it is asked about and refuses configured lanes.
type floorStub struct {
	name   string
	refuse map[conductor.LaneID]bool
	seen   []conductor.LaneID
}

func (s *floorStub) Name() string { return s.name }
func (s *floorStub) Admit(_ ResourceRequest, slot ProviderSlot) (bool, string) {
	s.seen = append(s.seen, slot.LaneID)
	if s.refuse[slot.LaneID] {
		return false, "below the floor"
	}
	return true, ""
}

// recordingSelector wraps a Selector and keeps the last input it saw.
type recordingSelector struct {
	inner Selector
	got   SelectInput
}

func (r *recordingSelector) Select(ctx context.Context, in SelectInput) (Selection, error) {
	r.got = in
	return r.inner.Select(ctx, in)
}

func TestFleetLeaseFloorsRunBeforeSelector(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	floor := &floorStub{name: "min-quality", refuse: map[conductor.LaneID]bool{"lane-t2": true}}
	rec := &recordingSelector{inner: f.deps.Selector}
	f.deps.Floors, f.deps.Selector = []LaneFilter{floor}, rec
	f.rebuild()
	l, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	if err != nil || l.LaneID != "lane-t1" {
		t.Fatalf("Lease = (%+v, %v), want lane-t1 once the floor removed lane-t2", l, err)
	}
	if !slices.Equal(rec.got.Eligible, []conductor.LaneID{"lane-t0", "lane-t1"}) {
		t.Fatalf("Selector saw Eligible %v, want [lane-t0 lane-t1]", rec.got.Eligible)
	}

	// The sensitivity filter runs first and no floor can add a lane back.
	f.seedJob("job-r", provider.SensitivityRestricted)
	floor.seen = nil
	req := request()
	req.Sensitivity = provider.SensitivityRestricted
	if _, err := f.fleet.Lease(t.Context(), req, dispatchFor("job-r"), 1); err != nil {
		t.Fatalf("restricted Lease: %v", err)
	}
	if !slices.Equal(floor.seen, []conductor.LaneID{"lane-t0"}) || !slices.Equal(rec.got.Eligible, []conductor.LaneID{"lane-t0"}) {
		t.Fatalf("floor saw %v, selector saw %v, want only lane-t0: the sensitivity filter ran first", floor.seen, rec.got.Eligible)
	}

	all := &floorStub{name: "everything-off", refuse: map[conductor.LaneID]bool{"lane-t0": true, "lane-t1": true, "lane-t2": true}}
	f.deps.Floors = []LaneFilter{all}
	f.rebuild()
	_, err = f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoCapableTier, "everything-off: below the floor", "lane-t0 (controller-local)")
}

// selectorFunc adapts a function to Selector.
type selectorFunc func(context.Context, SelectInput) (Selection, error)

func (s selectorFunc) Select(ctx context.Context, in SelectInput) (Selection, error) {
	return s(ctx, in)
}

func TestFleetLeaseSelectorRefusals(t *testing.T) {
	cases := map[string]struct {
		pick func(context.Context, SelectInput) (Selection, error)
		want error
		msg  string
	}{
		"no allowed lanes passes through unchanged": {func(context.Context, SelectInput) (Selection, error) {
			return Selection{}, ErrNoAllowedLanes
		}, ErrNoAllowedLanes, "disallowed by user-config lane policy"},
		"a typed capable-tier refusal passes through": {func(context.Context, SelectInput) (Selection, error) {
			return Selection{}, ErrNoCapableTier
		}, ErrNoCapableTier, "no capable tier can serve this request"},
		"an unknown selector failure is no capable tier": {func(context.Context, SelectInput) (Selection, error) {
			return Selection{}, cascade.New(cascade.KindInternal, "ranker crashed")
		}, ErrNoCapableTier, "selector failed: internal: ranker crashed"},
		"a lane outside the eligible set is refused": {func(context.Context, SelectInput) (Selection, error) {
			return Selection{Lane: "lane-elsewhere"}, nil
		}, ErrNoCapableTier, `lane "lane-elsewhere" outside the eligible set`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.seedJob("job-1", provider.SensitivityInternal)
			f.deps.Selector = selectorFunc(c.pick)
			f.rebuild()
			_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
			requireSentinel(t, err, c.want, c.msg)
			f.zeroRows()
		})
	}
}

func TestFleetLeaseSnapshotFailureIsNoCapableTier(t *testing.T) {
	f := newFx(t)
	f.seedJob("job-1", provider.SensitivityInternal)
	f.deps.Snapshot = func(context.Context) (FleetSnapshot, error) {
		return FleetSnapshot{}, cascade.New(cascade.KindUnavailable, "compositor down")
	}
	f.rebuild()
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
	requireSentinel(t, err, ErrNoCapableTier, "compositor down")
	f.zeroRows()
}

func TestFleetLeaseRefusesAMissingJob(t *testing.T) {
	f := newFx(t)
	_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-missing"), 1)
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("err = %v, want KindNotFound for a job that does not exist", err)
	}
	f.zeroRows()
}

func TestFleetLeaseRefusesLaneWithoutQuotaDomain(t *testing.T) {
	cases := map[string]struct {
		domainOf func(context.Context, conductor.LaneID) (string, error)
		want     error
	}{
		"not found": {func(context.Context, conductor.LaneID) (string, error) {
			return "", cascade.New(cascade.KindNotFound, "no domain")
		}, ErrNoQuotaDomain},
		"empty domain":  {func(context.Context, conductor.LaneID) (string, error) { return "", nil }, ErrNoQuotaDomain},
		"typed refusal": {func(context.Context, conductor.LaneID) (string, error) { return "", ErrNoQuotaDomain }, ErrNoQuotaDomain},
		"lookup failure": {func(context.Context, conductor.LaneID) (string, error) {
			return "", cascade.New(cascade.KindUnavailable, "topology db down")
		}, ErrReservationFailed},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.seedJob("job-1", provider.SensitivityInternal)
			f.deps.DomainOf = c.domainOf
			f.rebuild()
			_, err := f.fleet.Lease(t.Context(), request(), dispatchFor("job-1"), 1)
			requireSentinel(t, err, c.want, "lane-t2")
			if !cascade.HasKind(err, cascade.KindUnavailable) {
				t.Fatalf("err = %v, want KindUnavailable", err)
			}
			f.zeroRows()
		})
	}
}

func TestFleetLeaseDispatchRef(t *testing.T) {
	f := newFx(t)
	l, err := f.fleet.Lease(t.Context(), request(), DispatchRef{ProjectID: "project-1", Requests: 1}, 1)
	if err != nil || l.LaneID != "lane-t0" {
		t.Fatalf("job-less Lease = (%+v, %v), want lane-t0: with no job the data class reads local-only", l, err)
	}
	if n := f.count("jobs_execution"); n != 0 {
		t.Fatalf("jobs_execution has %d rows, want 0 for an empty JobID", n)
	}
	if r, ok, err := f.fleet.ReservationByExecution(t.Context(), l.ExecutionID); err != nil || !ok || r.ExecutionID != l.ExecutionID {
		t.Fatalf("reservation = (%+v, %v, %v), want the minted execution id", r, ok, err)
	}

	f.seedJob("job-1", provider.SensitivityInternal)
	for name, d := range map[string]DispatchRef{
		"no job":  {ProjectID: "p", Requests: 1, RepoID: "repo-a", ScopeGlobs: []string{"pkg/**"}},
		"no repo": {ProjectID: "p", Requests: 1, JobID: "job-1", ScopeGlobs: []string{"pkg/**"}},
	} {
		before := f.count("jobs_reservation")
		if _, err := f.fleet.Lease(t.Context(), request(), d, 1); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: err = %v, want KindInvalidInput", name, err)
		}
		if after := f.count("jobs_reservation"); after != before || len(f.calls.all()) != 0 {
			t.Fatalf("%s: rows %d->%d, calls %v, want no write", name, before, after, f.calls.all())
		}
	}

	d := dispatchFor("job-1")
	d.RepoID, d.ScopeGlobs = "repo-a", []string{"pkg/**"}
	got, err := f.fleet.Lease(t.Context(), request(), d, 1)
	if err != nil || !slices.Equal(got.RepoLeaseIDs, []string{"lease-1"}) || f.book.liveCount() != 1 {
		t.Fatalf("Lease with ScopeGlobs = (%+v, %v), want the one lease of the reservation held", got, err)
	}
	_, err = f.fleet.Lease(t.Context(), request(), d, 1)
	requireSentinel(t, err, ErrReservationFailed, `glob "pkg/**" is held by lease "lease-1"`)
	if rows := f.count("jobs_execution"); rows != 1 || f.book.liveCount() != 1 {
		t.Fatalf("after contention: jobs_execution %d, live leases %d, want only the first lease's (1 and 1)", rows, f.book.liveCount())
	}
	rs, lerr := f.store.ListByState(t.Context(), economics.ReservationRolledBack)
	if lerr != nil || len(rs) != 1 {
		t.Fatalf("rolled_back rows = (%d, %v), want the contended reservation", len(rs), lerr)
	}
}

func TestCapacityImportsNoLearn(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.HasSuffix(dep, "/internal/learn") || strings.Contains(dep, "/internal/learn/") {
			t.Fatalf("capacity depends on %s: capacity must not import internal/learn", dep)
		}
	}
	if !strings.Contains(string(out), "/internal/fleet/capacity") {
		t.Fatalf("go list -deps did not list the package itself:\n%s", out)
	}
}
