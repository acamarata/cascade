package daemon

// Purpose: proves the topology step: ReconcileNow runs its seven steps in
// order (agent rows, seeded buckets, a pin applied then restored, a
// promotion), a step error names the step and keeps earlier rows, Health is
// HealthKeys over the live rows, Topology is the capacity step's store, a
// changing pass publishes one reconciled event, and the bind refusals.
// SPORT: internal/daemon (ADD, P1-TOP-06e).

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// agentLaneID is the lane ReconcileAgentLanes derives for a harness.
func agentLaneID(harness string) topology.LaneID {
	id := "agent:" + harness
	return topology.LaneIDFor(topology.RuntimeProfileID(id), topology.DomainID(id), "", "", topology.EffortNA, topology.InteractionInteractive)
}

// passingSource reports one posterior that clears the promotion rule.
type passingSource struct{}

func (passingSource) LanePosteriors(context.Context, topology.LaneID) ([]topology.Posterior, error) {
	return []topology.Posterior{{TaskClass: topology.TaskClassClassify, Samples: 50, Mean: 0.8}}, nil
}

func agentRigDeps(k *topoRig) {
	k.deps.AgentLanes = func() []topology.AgentLaneRecord {
		return []topology.AgentLaneRecord{{LaneName: "claude-cli-1", Harness: string(topology.RuntimeClaudeCLI)}}
	}
}

func TestReconcileNowRunsEveryStepInOrder(t *testing.T) {
	pinned := agentLaneID(string(topology.RuntimeClaudeCLI))
	until := topoEpoch.Add(48 * time.Hour)
	k := newTopoRig(t, `[fleet.lanes."`+string(pinned)+`"]
lane_class = "worker-fast"
reversal_until = "`+until.Format(time.RFC3339)+`"
reversal_reason = "operator pin"
`)
	dom := k.seedSubscription(t, "anthropic-1")
	agentRigDeps(k)
	k.bind(t)
	if err := k.r.SetPosteriorSource(passingSource{}); err != nil {
		t.Fatalf("SetPosteriorSource: %v", err)
	}
	var order []string
	k.r.topo.beforeStep = func(name string) error { order = append(order, name); return nil }
	if rep := k.pass(t); rep.RowsWritten == 0 || rep.LanesReconciled != 1 {
		t.Fatalf("first pass = %+v, want rows written and one registry lane", rep)
	}
	want := []string{"reconcile", "agent-lanes", "seed-buckets", "observation-buckets", "window-buckets", "lane-pins", "promotions"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("step order = %v, want %v", order, want)
	}
	store := k.r.Topology()
	if _, err := store.GetAccount(k.ctx, "agent:claude-cli"); err != nil {
		t.Fatalf("agent account row: %v", err)
	}
	if bs, err := k.r.Quota().ListBuckets(k.ctx, dom); err != nil || len(bs) != 4 {
		t.Fatalf("seeded buckets = %d, %v; want 4", len(bs), err)
	}
	if lane, err := store.GetLane(k.ctx, pinned); err != nil || lane.LaneClass != topology.LaneClassWorkerFast {
		t.Fatalf("pinned lane = %+v, %v; want worker-fast", lane, err)
	}
	assertPromotedRegistryLane(t, k, pinned)
	k.g.clock.Set(until.Add(time.Hour))
	k.pass(t)
	revs, err := store.ListReversals(k.ctx, pinned)
	if err != nil || len(revs) != 1 || revs[0].RestoredAt == nil {
		t.Fatalf("reversals after the clock jump = %+v, %v; want one restored row", revs, err)
	}
	if lane, _ := store.GetLane(k.ctx, pinned); lane.LaneClass == topology.LaneClassWorkerFast {
		t.Fatalf("pinned lane still worker-fast after expiry: %+v", lane)
	}
	assertAuditKinds(t, k, topology.PromotionEventPromoted, topology.PromotionEventReversed, topology.PromotionEventReversalExpiry)
	assertStepErrorKeepsEarlierRows(t)
}

// assertStepErrorKeepsEarlierRows injects a step-3 error into a fresh pass.
func assertStepErrorKeepsEarlierRows(t *testing.T) {
	t.Helper()
	e := newTopoRig(t, "")
	e.seedSubscription(t, "anthropic-1")
	e.bind(t)
	injected := errors.New("injected seed failure")
	e.r.topo.beforeStep = func(name string) error {
		if name == "seed-buckets" {
			return injected
		}
		return nil
	}
	rep, err := e.r.ReconcileNow(e.ctx)
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), `step "seed-buckets"`) || rep.RowsWritten == 0 {
		t.Fatalf("injected step-3 error = %+v, %v; want the step named and step 1 rows counted", rep, err)
	}
	if lanes, err := e.r.Topology().ListLanes(e.ctx, false); err != nil || len(lanes) != 1 {
		t.Fatalf("step 1 lanes after the failure = %d, %v; want 1", len(lanes), err)
	}
}

func assertPromotedRegistryLane(t *testing.T, k *topoRig, pinned topology.LaneID) {
	t.Helper()
	lanes, err := k.r.Topology().ListLanes(k.ctx, false)
	if err != nil || len(lanes) != 2 {
		t.Fatalf("live lanes = %d, %v; want the registry and agent lanes", len(lanes), err)
	}
	for _, l := range lanes {
		if l.ID != pinned && l.LaneClass != topology.PromotionDefaultClass {
			t.Fatalf("registry lane = %+v, want promoted to %s", l, topology.PromotionDefaultClass)
		}
	}
}

func assertAuditKinds(t *testing.T, k *topoRig, kinds ...topology.PromotionEventKind) {
	t.Helper()
	evs, err := k.g.deps.Bus.Replay(k.ctx, fleetTopologyAuditNamespace, 0)
	if err != nil {
		t.Fatalf("Replay audit: %v", err)
	}
	seen := map[topology.PromotionEventKind]bool{}
	for _, ev := range evs {
		var pe topology.PromotionEvent
		if ev.Kind != fleetTopologyPromotionKind || json.Unmarshal(ev.Payload, &pe) != nil {
			t.Fatalf("audit event %+v is not a promotion event", ev)
		}
		seen[pe.Kind] = true
	}
	for _, kind := range kinds {
		if !seen[kind] {
			t.Fatalf("audit events %v lack %q", seen, kind)
		}
	}
}

func TestFleetHealthIsHealthKeys(t *testing.T) {
	k := newTopoRig(t, "")
	k.seedSubscription(t, "anthropic-1")
	k.bind(t)
	k.pass(t)
	got, err := k.r.Health(k.ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	store := k.r.Topology()
	lanes, _ := store.ListLanes(k.ctx, false)
	domains, _ := store.ListQuotaDomains(k.ctx)
	accts, _ := store.ListAccounts(k.ctx)
	byID := map[topology.AccountID]topology.Account{}
	for _, a := range accts {
		byID[a.ID] = a
	}
	snap, err := topology.TakeQuotaSnapshot(k.ctx, k.r.Quota(), domains, k.g.clock.Now())
	if err != nil {
		t.Fatalf("TakeQuotaSnapshot: %v", err)
	}
	want := economics.HealthKeys(snap, lanes, domains, byID, k.g.clock)
	if len(got) != 5 || !reflect.DeepEqual(got, want) {
		t.Fatalf("Health = %+v, want HealthKeys %+v over five keys", got, want)
	}
	if _, err := k.r.JobsDB().ExecContext(k.ctx, `ALTER TABLE config_lane RENAME TO config_lane_hidden`); err != nil {
		t.Fatalf("hide lanes: %v", err)
	}
	got, err = k.r.Health(k.ctx)
	if got != nil || !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "no such table") {
		t.Fatalf("Health on a read error = %v, %v; want (nil, the read error)", got, err)
	}
	if _, err := k.r.JobsDB().ExecContext(k.ctx, `ALTER TABLE config_lane_hidden RENAME TO config_lane`); err != nil {
		t.Fatalf("restore lanes: %v", err)
	}
}

func TestTopologyIsCapacityStore(t *testing.T) {
	k := newTopoRig(t, "")
	if k.r.Topology() == nil || k.r.Topology() != k.r.CapacityParts().Domains.Topology || k.r.Topology() != k.r.res.topo {
		t.Fatalf("Topology() = %p, capacity domains store %p", k.r.Topology(), k.r.CapacityParts().Domains.Topology)
	}
}

func TestReconcileEventPublished(t *testing.T) {
	k := newTopoRig(t, "")
	k.seedSubscription(t, "anthropic-1")
	k.bind(t)
	rep := k.pass(t)
	evs := reconciledEvents(t, k)
	if len(evs) != 1 || evs[0].Kind != fleetTopologyReconciledKind {
		t.Fatalf("reconciled events = %+v, want one", evs)
	}
	var body map[string]any
	if err := json.Unmarshal(evs[0].Payload, &body); err != nil {
		t.Fatalf("payload: %v", err)
	}
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"changed", "generation", "lanes", "schema_version"}) || strings.Contains(string(evs[0].Payload), "vault") {
		t.Fatalf("payload fields = %v (%s), want only the reconciled shape", keys, evs[0].Payload)
	}
	if body["generation"] != 1.0 || body["lanes"] != 1.0 || body["changed"] != float64(rep.RowsWritten) || rep.RowsWritten == 0 {
		t.Fatalf("payload = %v, report %+v; want generation 1, one lane, changed = rows written > 0", body, rep)
	}
	if again := k.pass(t); again.RowsWritten != 0 {
		t.Fatalf("unchanged pass = %+v, want RowsWritten 0", again)
	}
	if evs := reconciledEvents(t, k); len(evs) != 1 {
		t.Fatalf("an unchanged pass published: %+v", evs)
	}
}

func reconciledEvents(t *testing.T, k *topoRig) []events.Event {
	t.Helper()
	evs, err := k.g.deps.Bus.Replay(k.ctx, fleetTopologyNamespace, 0)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return evs
}

func TestBindTopologyPassRefusals(t *testing.T) {
	k := newTopoRig(t, "")
	k.seedSubscription(t, "anthropic-1")
	if _, err := k.r.ReconcileNow(k.ctx); !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "before BindTopologyPass") {
		t.Fatalf("ReconcileNow before bind = %v, want KindUnavailable", err)
	}
	for _, field := range []string{"Registry", "AgentLanes", "Usage", "QuotaWindows", "Bus", "Clock"} {
		d := k.deps
		reflect.ValueOf(&d).Elem().FieldByName(field).Set(reflect.Zero(reflect.ValueOf(d).FieldByName(field).Type()))
		if err := k.r.BindTopologyPass(d); !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), field+" is nil") {
			t.Fatalf("nil %s = %v, want KindInvalidInput", field, err)
		}
	}
	assertNoLanes(t, k)
	if err := k.r.SetPosteriorSource(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("SetPosteriorSource(nil) = %v, want KindInvalidInput", err)
	}
	if err := k.r.SetPosteriorSource(passingSource{}); err != nil {
		t.Fatalf("SetPosteriorSource: %v", err)
	}
	if err := k.r.SetPosteriorSource(passingSource{}); !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "already set") {
		t.Fatalf("second SetPosteriorSource = %v, want KindConflict", err)
	}
	k.bind(t)
	if err := k.r.BindTopologyPass(k.deps); !cascade.HasKind(err, cascade.KindConflict) || !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("second BindTopologyPass = %v, want KindConflict", err)
	}

	bad := newTopoRig(t, "[fleet.lanes.\"lane-a\"]\nlane_class = \"not-a-class\"\nreversal_until = \"2026-11-05T00:00:00Z\"\nreversal_reason = \"x\"\n")
	bad.seedSubscription(t, "anthropic-1")
	err := bad.r.BindTopologyPass(bad.deps)
	if !errors.Is(err, cascade.ErrInvalidInput) || !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "unknown lane class") {
		t.Fatalf("bad pin = %v, want KindInvalidInput naming the class", err)
	}
	assertNoLanes(t, bad)
	if _, err := bad.r.ReconcileNow(bad.ctx); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("a refused bind left the pass bound: %v", err)
	}
}

func assertNoLanes(t *testing.T, k *topoRig) {
	t.Helper()
	if lanes, err := k.r.Topology().ListLanes(k.ctx, true); err != nil || len(lanes) != 0 {
		t.Fatalf("lanes after a refused bind = %d, %v; want zero rows", len(lanes), err)
	}
}
