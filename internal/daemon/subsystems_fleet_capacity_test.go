package daemon

// Purpose: the capacity step composes the production Fleet over the learn
//
//	adapters, the interim selector, the registry-lane domain lookup and node
//	placement, and its supervised refresh reads the registry, the node
//	records and the sampler on every tick and stops with the run context.
//	A fake ticker drives the refresh; no sleeps.
//
// SPORT: internal/daemon (ADD, P1-CAP-05).

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// newCapRig starts a fleet runtime whose capacity refresh runs on a fake ticker.
func newCapRig(t *testing.T) (*fleetRig, *FleetRuntime, *fleetFakeTicker) {
	t.Helper()
	capT := newFleetFakeTicker()
	saved := newCapacityTicker
	newCapacityTicker = func(time.Duration) runtime.Ticker { return capT }
	t.Cleanup(func() { newCapacityTicker = saved })
	g := newFleetRig(t, "")
	r := g.start(t)
	capT.awaitPoll(t)
	return g, r, capT
}

func TestFleetCapacityStepComposes(t *testing.T) {
	g, r, _ := newCapRig(t)
	p := r.CapacityParts()
	if r.Fleet() == nil || p.Compositor == nil || p.Registry == nil || p.Nodes == nil || p.Scorer == nil {
		t.Fatalf("capacity parts incomplete: fleet %v, parts %+v", r.Fleet(), p)
	}
	if p.Policy.Estimator != capacity.EstimateSource(p.Estimates) || p.Deps.Selector != capacity.NewTierPolicySelector(p.Policy) {
		t.Fatal("the selector is not the interim TierPolicy selector over the learn estimate source")
	}
	if _, ok := p.Policy.Probes.(*capacity.DefaultProbeAdmission); !ok {
		t.Fatalf("policy probes = %T, want *capacity.DefaultProbeAdmission", p.Policy.Probes)
	}
	if p.Domains.Topology != r.res.topo || p.Placer.Records != p.Nodes || p.ControllerNodeID != localControllerNodeID {
		t.Fatalf("domains %p (want %p), placer records %p (want %p), controller %q", p.Domains.Topology, r.res.topo, p.Placer.Records, p.Nodes, p.ControllerNodeID)
	}
	var name string
	err := r.JobsDB().QueryRowContext(g.ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='jobs_scheduler_decisions'`).Scan(&name)
	if err != nil {
		t.Fatalf("learn decision table on the runtime handle: %v", err)
	}
	info, err := os.Stat(p.SaltPath)
	if err != nil || info.Size() != 32 {
		t.Fatalf("aggregate salt = %v, %v; want a 32-byte file", info, err)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("aggregate salt mode = %v, want 0600", info.Mode().Perm())
	}
	if row := g.row(fleetCapacityRefreshSubsystem); row.State != SubsystemRunning {
		t.Fatalf("refresh row = %+v, want running", row)
	}
}

func TestFleetCapacityRefreshReadsSourcesAndJoins(t *testing.T) {
	g, r, capT := newCapRig(t)
	p := r.CapacityParts()
	if _, ok := p.Compositor.Snapshot().Nodes[localControllerNodeID]; !ok {
		t.Fatal("the build's first refresh did not read the sampler into the self node")
	}
	rec := registry.ProviderRecord{Name: "prov-cap", Driver: registry.DriverOpenAICompat, BaseURL: "https://example.invalid/v1",
		Auth: registry.AuthKey, AuthRef: "provider.prov-cap.key", AccountKind: registry.AccountPersonal,
		Tier: registry.TierMid, HealthStatus: registry.HealthUnknown}
	if err := p.Registry.UpsertProvider(g.ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if _, ok := p.Compositor.Snapshot().Providers["prov-cap"]; ok {
		t.Fatal("the provider appeared before any refresh tick")
	}
	capT.tick(t)
	if _, ok := p.Compositor.Snapshot().Providers["prov-cap"]; !ok {
		t.Fatal("one refresh tick did not read the registry")
	}
	g.cancel()
	g.awaitJoin(t)
	if row := g.row(fleetCapacityRefreshSubsystem); row.State != SubsystemSkipped {
		t.Fatalf("refresh row after the run = %+v, want skipped/stopped", row)
	}
}

func TestFleetCapacityDomainOfRefusesNoQuotaDomain(t *testing.T) {
	g, r, _ := newCapRig(t)
	domain, err := r.CapacityParts().Domains.DomainOf(g.ctx, "never-reconciled")
	if err == nil || domain != "" || !hasIdentity(err, capacity.ErrNoQuotaDomain) {
		t.Fatalf("DomainOf(un-reconciled) = %q, %v; want capacity.ErrNoQuotaDomain", domain, err)
	}
	if _, err := r.CapacityParts().Domains.DomainOf(g.ctx, "agent:worker"); !hasIdentity(err, capacity.ErrNoQuotaDomain) {
		t.Fatalf("DomainOf(agent lane) = %v; want capacity.ErrNoQuotaDomain (agent lanes have no quota domain yet)", err)
	}
}

func TestFleetCapacityPlaceFailsClosed(t *testing.T) {
	g, r, _ := newCapRig(t)
	p := r.CapacityParts()
	need := nodes.Requirement{Sensitivity: provider.SensitivityPublic}
	if node, err := p.Placer.Place(g.ctx, need); err == nil || node != "" {
		t.Fatalf("Place with no enrolled node = %q, %v; want a refusal", node, err)
	}
	id, _, err := nodes.GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Nodes.Enroll(id, nodes.TierWorkerTrusted); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	// Enrolled but with no live tunnel (SetNodeTunnels never called): still refused.
	if node, err := p.Placer.Place(g.ctx, need); err == nil || node != "" {
		t.Fatalf("Place with an unconnected node = %q, %v; want a refusal", node, err)
	}
	// A registered lookup never turns an unreachable node into a placement.
	r.SetNodeTunnels(func(string) nodes.TunnelState { return nodes.TunnelUp })
	if node, err := p.Placer.Place(g.ctx, need); err == nil || node != "" {
		t.Fatalf("Place with a tunnel up but no verified heartbeat = %q, %v; want a refusal", node, err)
	}
	r.SetNodeTunnels(nil)
}

func TestFleetCapacityControllerFromBinding(t *testing.T) {
	bound := fakeBindingBackend{ok: true, binding: nodes.ControllerBinding{Endpoint: "ctl.example.invalid:7000", EnrollmentID: "enroll-1"}}
	if ctl, self, err := controllerNodeIDs(bound); err != nil || ctl != "ctl.example.invalid:7000" || self != "enroll-1" {
		t.Fatalf("bound = %q, %q, %v; want the endpoint and the enrollment id", ctl, self, err)
	}
	if ctl, self, err := controllerNodeIDs(fakeBindingBackend{}); err != nil || ctl != localControllerNodeID || self != ctl {
		t.Fatalf("unbound = %q, %q, %v; want this daemon as the controller", ctl, self, err)
	}
	if _, _, err := controllerNodeIDs(fakeBindingBackend{err: errors.New("corrupt")}); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("unreadable binding = %v, want KindUnavailable", err)
	}
}

// recordingExecer captures one ExecContext call.
type recordingExecer struct{ args []any }

func (e *recordingExecer) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	e.args = args
	return nil, nil
}

func TestFleetCapacityDecisionRecorderMapsRecord(t *testing.T) {
	_, r, _ := newCapRig(t)
	q := &recordingExecer{}
	d := capacity.DecisionRecord{ID: "dec-1", JobID: "job-1", ExecutionID: "exec-1", TaskClass: "code", SelectedTier: capacity.TierOne,
		LaneID: "lane-1", NodeID: "controller", ScoreAtSelection: 0.5, FallbackLevel: "global",
		JumpReasonCode: capacity.JumpReasonNone, DecidedAt: time.Unix(10_000, 0)}
	if err := r.CapacityParts().Deps.Decisions.Record(context.Background(), q, d); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(q.args) < 7 || q.args[0] != "dec-1" || q.args[5] != "lane-1" || q.args[6] != "controller" {
		t.Fatalf("recorded args = %v, want the decision id, lane and node in column order", q.args)
	}
}

func TestFleetCapacityAgentLanesMergeIntoSnapshot(t *testing.T) {
	g, r, _ := newCapRig(t)
	lane := capacity.AgentLane{DriverID: "codex", LaneType: conductor.LaneControllerLocal}
	if snap, err := r.capa.snapshot(g.clock); err != nil || len(snap.Lanes) != 0 {
		t.Fatalf("snapshot before SetAgentLanes = %d lanes, %v; want none", len(snap.Lanes), err)
	}
	r.SetAgentLanes(func() []capacity.AgentLane { return []capacity.AgentLane{lane} })
	snap, err := r.capa.snapshot(g.clock)
	slot, ok := snap.Lanes["agent:codex"]
	if err != nil || !ok || slot.Tier != capacity.TierZero || slot.State != capacity.StateAvailable {
		t.Fatalf("agent lane slot = %+v (%v), %v; want an available tier-zero slot", slot, ok, err)
	}
	if r.Compositor().Snapshot().Lanes["agent:codex"].LaneID != "" {
		t.Fatal("the merge leaked into the compositor's own snapshot")
	}
	r.SetAgentLanes(func() []capacity.AgentLane { return []capacity.AgentLane{{}} })
	if _, err := r.capa.snapshot(g.clock); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("an agent lane with no driver = %v, want KindInvalidInput", err)
	}
	r.SetAgentLanes(nil)
	if snap, err := r.capa.snapshot(g.clock); err != nil || len(snap.Lanes) != 0 {
		t.Fatalf("snapshot after SetAgentLanes(nil) = %d lanes, %v; want none", len(snap.Lanes), err)
	}
}

// syncBuf is a goroutine-safe log sink.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestFleetCapacityAggregationRunsOnTick(t *testing.T) {
	aggT := newFleetFakeTicker()
	saved := newAggregateTicker
	newAggregateTicker = func(time.Duration) runtime.Ticker { return aggT }
	t.Cleanup(func() { newAggregateTicker = saved })
	var logs syncBuf
	g := newFleetRig(t, "")
	g.deps.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	r := g.start(t)
	aggT.awaitPoll(t)
	aggT.tick(t)
	if strings.Contains(logs.String(), "fleet learn aggregation") {
		t.Fatalf("aggregation over the created salt failed: %s", logs.String())
	}
	if err := os.Remove(r.CapacityParts().SaltPath); err != nil {
		t.Fatalf("remove salt: %v", err)
	}
	aggT.tick(t) // the second tick runs Aggregate, which refuses without a salt
	if !strings.Contains(logs.String(), "fleet learn aggregation") {
		t.Fatalf("the missing salt was not reported by the aggregation tick: %q", logs.String())
	}
	g.cancel()
	g.awaitJoin(t)
	if row := g.row(fleetLearnAggregateSubsystem); row.State != SubsystemSkipped {
		t.Fatalf("aggregation row after the run = %+v, want skipped/stopped", row)
	}
}
