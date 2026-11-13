//go:build !windows

// Purpose: the fleet-topology wiring: the composed daemon's topology rows
// exist before serve and back real leases, Reconcile, NewWindowAccountant and
// HealthKeys have no production caller outside the topology mount, an
// unreadable registry fails closed, and a [fleet.lanes] pin is applied,
// idempotent, audited on the daemon bus and restored; an invalid pin refuses start.
// SPORT: cmd/cascade/daemon (ADD, P1-TOP-06f).
package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// agentTopologyLane is the claude-cli agent lane's topology id.
var agentTopologyLane = topology.LaneIDFor("agent:claude-cli", "agent:claude-cli", "", "",
	topology.EffortNA, topology.InteractionInteractive)

// want fails the test with the message unless ok.
func want(t *testing.T, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf(format, args...)
	}
}

// composeTopology composes a daemon over a fresh HOME with config written
// first; seed runs in phaseFleet just before the topology mount.
func composeTopologyPass(t *testing.T, config string, clock runtime.Clock, seed func(*daemonWiring)) (capDaemon, error) {
	t.Helper()
	for _, k := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(k, t.TempDir())
	}
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	if clock != nil {
		deps.Clock = clock
	}
	if config != "" {
		want(t, os.WriteFile(deps.Paths.ConfigPath(), []byte(config), 0o600) == nil, "write config")
	}
	addTestRegistration(t, daemonRegistration{Name: "test-topology-seed", Phase: phaseFleet, Order: 59,
		Wire: func(w *daemonWiring) error { seed(w); return nil }})
	seen := observeWiring(t)
	_, cleanups, err := composeDaemon(context.Background(), deps, func(*rpc.Registry, *rpc.SSEMux) {})
	if t.Cleanup(func() { runCleanupsLIFO(cleanups) }); err != nil {
		return capDaemon{}, err
	}
	w := <-seen
	return capDaemon{c: composedForTest{Opts: daemon.RunOptions{Manifest: w.Manifest}}, bus: w.Bus,
		rt: w.Deps.Fleet, parts: w.Deps.Fleet.CapacityParts()}, nil
}

// runningTopology composes the daemon and wants the pre-serve pass started.
func runningTopology(t *testing.T, config string, clock runtime.Clock, seed func(*daemonWiring)) capDaemon {
	t.Helper()
	d, err := composeTopologyPass(t, config, clock, seed)
	want(t, err == nil, "composeDaemon: %v", err)
	r := manifestRow(d, fleetTopologySubsystem)
	want(t, r.State == daemon.SubsystemRunning, "fleet-topology = %+v, want running after the pre-serve pass", r)
	return d
}

// seedTopologyFleet stores a key and an oauth provider and, unless noAgent,
// one agent lane.
func seedTopologyFleet(t *testing.T, noAgent ...bool) func(*daemonWiring) {
	return func(w *daemonWiring) {
		reg := w.Deps.Fleet.CapacityParts().Registry
		for name, auth := range map[string]registry.AuthType{"key": registry.AuthKey, "oauth": registry.AuthOAuth} {
			seedCapProvider(t, reg, registry.ProviderRecord{Name: name + "-prov", Driver: registry.DriverOpenAICompat,
				BaseURL: "https://example.invalid/v1", Auth: auth, Tier: registry.TierMid},
				registry.LaneRecord{LaneName: name + "-lane-1"})
		}
		if len(noAgent) == 0 {
			w.Deps.Fleet.SetAgentLanes(func() []capacity.AgentLane {
				return []capacity.AgentLane{{DriverID: string(topology.RuntimeClaudeCLI), LaneType: conductor.LaneControllerLocal,
					Posture: provider.NewCompliancePosture(nil, false, true, nil, "", false)}}
			})
		}
	}
}

// manifestRow returns the named manifest row.
func manifestRow(d capDaemon, name string) daemon.SubsystemStatus {
	snap := d.c.Opts.Manifest.Snapshot()
	if i := slices.IndexFunc(snap, func(s daemon.SubsystemStatus) bool { return s.Name == name }); i >= 0 {
		return snap[i]
	}
	return daemon.SubsystemStatus{}
}

// fundDomain writes roomy rpm/tpm/rpd buckets on domain (no Reconcile).
func fundDomain(t *testing.T, d capDaemon, domain topology.DomainID) {
	t.Helper()
	ctx, store := context.Background(), d.rt.Topology()
	dom, err := store.GetQuotaDomain(ctx, domain)
	acct, aerr := store.GetAccount(ctx, dom.AccountRef)
	want(t, err == nil && aerr == nil, "domain %s after the mounted pass: %v, %v", domain, err, aerr)
	for _, dim := range []string{topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD} {
		b := topology.Bucket{Name: dim, Limit: 1_000_000, RemainingFraction: 1, Window: topology.BucketWindowDay,
			Source: topology.SourceProviderStatus, Confidence: 1, LimitScopeID: topology.ResolveScope(acct, dom),
			CapacityObserved: 1_000_000, WindowID: "w1", Version: 1, ObservedAt: time.Now()}
		err := d.rt.Quota().UpsertBucket(ctx, dom.ID, dim, b)
		want(t, err == nil, "UpsertBucket(%s): %v", dim, err)
	}
}

// agentDomainFleet is a Fleet over the composed deps whose DomainOf resolves an
// agent: lane through its topology credential row; the production adapter
// refuses every agent: lane (DEBT P1-AGT-08, internal/daemon).
func agentDomainFleet(t *testing.T, d capDaemon) *capacity.Fleet {
	t.Helper()
	deps := d.parts.Deps
	deps.DomainOf = func(ctx context.Context, lane conductor.LaneID) (string, error) {
		if !strings.HasPrefix(string(lane), "agent:") {
			return d.parts.Domains.DomainOf(ctx, lane)
		}
		id, err := d.rt.Topology().DomainForCredential(ctx, topology.CredentialID(lane))
		return string(id), err
	}
	f, err := capacity.NewFleet(deps)
	want(t, err == nil, "NewFleet: %v", err)
	return f
}

// topoLease leases public work for a fresh public job on fleet (agent-only if agent).
func topoLease(t *testing.T, d capDaemon, fleet *capacity.Fleet, id string, agent bool) (capacity.Lease, error) {
	t.Helper()
	ctx, public := context.Background(), provider.SensitivityPublic
	j := jobs.Job{ID: id, State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1, RiskClass: string(jobs.RiskClassLow),
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: &public}
	want(t, d.rt.Jobs().PutJob(ctx, j) == nil, "PutJob(%s)", id)
	req := capacity.ResourceRequest{Intent: "p1-top-06f test", TaskClass: conductor.TaskClassCode, MinQuality: capacity.QualityStandard,
		Deadline: capacity.DeadlineInteractive, Sensitivity: public, RiskClass: jobs.RiskClassLow, DataClass: &public}
	return fleet.Lease(ctx, req,
		capacity.DispatchRef{JobID: id, ProjectID: "project-topo", AgentOnly: agent, HydratedTokens: 100, Requests: 1}, 1)
}

func TestReconcileMountedAtDaemonStart(t *testing.T) {
	d := runningTopology(t, "", nil, seedTopologyFleet(t))
	ctx := context.Background()
	domains, err := d.rt.Topology().ListQuotaDomains(ctx)
	for _, prefix := range []string{"key-prov", "oauth-prov", "agent:claude-cli"} {
		ok := slices.ContainsFunc(domains, func(d topology.QuotaDomain) bool { return strings.HasPrefix(string(d.ID), prefix) })
		want(t, ok, "domains before serve = %+v, %v; want one for %s", domains, err, prefix)
	}
	_, err = d.rt.Topology().GetLane(ctx, agentTopologyLane)
	want(t, err == nil, "agent lane row before serve: %v", err)
	fundDomain(t, d, "key-prov:api")
	awaitLane(t, d, "key-lane-1")
	lease, err := topoLease(t, d, d.rt.Fleet(), "job-topo-key", false)
	if err == nil {
		err = d.rt.Fleet().Release(ctx, lease)
	}
	want(t, err == nil, "public Lease and Release: %v", err)
	rsv, found, err := d.rt.Fleet().ReservationByExecution(ctx, lease.ExecutionID)
	want(t, err == nil && found && rsv.State != economics.ReservationHeld,
		"reservation after Release = %+v (found %v, %v), want a non-held row", rsv, found, err)
	fundDomain(t, d, "agent:claude-cli")
	fleet := d.rt.Fleet()
	agent, err := topoLease(t, d, fleet, "job-topo-agent", true)
	if isNoQuotaDomain(err) && strings.Contains(err.Error(), `lane "agent:claude-cli"`) {
		fleet = agentDomainFleet(t, d) // the production adapter's agent: refusal is not this ticket's
		agent, err = topoLease(t, d, fleet, "job-topo-agent-2", true)
	}
	want(t, err == nil && agent.LaneID == "agent:claude-cli", "AgentOnly Lease = %+v, %v; want the agent lane", agent, err)
	want(t, fleet.Release(ctx, agent) == nil, "Release(agent)")
}

func TestTopologyMountSoleCallers(t *testing.T) {
	guarded := map[string]string{"Reconcile": "topology", "NewWindowAccountant": "economics", "HealthKeys": "economics"}
	allowed := map[string]bool{"internal/daemon/subsystems_fleet_topology.go": true, "internal/daemon/topology_observations.go": true}
	root, fset, found := filepath.Join("..", ".."), token.NewFileSet(), map[string]int{}
	err := filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, isIdent := sel.X.(*ast.Ident); isIdent && guarded[sel.Sel.Name] == x.Name {
				if !allowed[filepath.ToSlash(rel)] {
					t.Errorf("%s: %s.%s called outside the topology mount", rel, x.Name, sel.Sel.Name)
				}
				found[sel.Sel.Name]++
			}
			return true
		})
		return nil
	})
	want(t, err == nil, "walk: %v", err)
	for name := range guarded {
		want(t, found[name] > 0, "no production caller of %s found: the scan is broken", name)
	}
}

func TestReconcileRegistryUnreadableFailsClosed(t *testing.T) {
	saved := topologyRegistry
	topologyRegistry = func(*daemon.FleetRuntime) (*registry.Registry, error) {
		return nil, cascade.New(cascade.KindUnavailable, "injected registry open failure")
	}
	t.Cleanup(func() { topologyRegistry = saved })
	d, err := composeTopologyPass(t, "", nil, seedTopologyFleet(t))
	want(t, err == nil, "composeDaemon must keep serving: %v", err)
	r := manifestRow(d, fleetTopologySubsystem)
	want(t, r.State == daemon.SubsystemError && strings.Contains(r.Detail, "injected"), "fleet-topology = %+v, want failed naming the cause", r)
	lanes, err := d.rt.Topology().ListLanes(context.Background(), true)
	want(t, err == nil && len(lanes) == 0, "topology lanes = %d, %v; want no row", len(lanes), err)
	awaitLane(t, d, "key-lane-1")
	for _, agent := range []bool{false, true} { // agent: domains resolve as in the mounted test
		_, err := topoLease(t, d, agentDomainFleet(t, d), fmt.Sprintf("job-topo-%v", agent), agent)
		want(t, isNoQuotaDomain(err), "Lease (agent=%v) = %v, want capacity.ErrNoQuotaDomain", agent, err)
	}
}

func TestLaneReversalConfigPin(t *testing.T) {
	t.Run("invalid pin refuses start", func(t *testing.T) {
		bad := "[fleet.lanes.\"lane-a\"]\nlane_class = \"not-a-class\"\nreversal_until = \"2026-11-05T00:00:00Z\"\nreversal_reason = \"x\"\n"
		_, err := composeTopologyPass(t, bad, nil, seedTopologyFleet(t))
		want(t, cascade.HasKind(err, cascade.KindInvalidInput), "invalid pin start = %v, want KindInvalidInput refusing start", err)
	})
	// A lane id is a pure hash of its rows, so a probe daemon names the key lane.
	// No agent lane here: Reconcile retires and step 2 revives one on every pass.
	var laneA topology.LaneID
	want(t, t.Run("probe", func(t *testing.T) {
		d := runningTopology(t, "", nil, seedTopologyFleet(t, true))
		lanes, err := d.rt.Topology().ListLanes(context.Background(), false)
		i := slices.IndexFunc(lanes, func(l topology.Lane) bool { return l.QuotaDomainRef == "key-prov:api" })
		want(t, err == nil && i >= 0, "probe daemon lanes = %+v, %v; want the key lane", lanes, err)
		laneA = lanes[i].ID
	}), "no probe lane")
	clock := testkit.NewFrozenClock(time.Now().UTC().Truncate(time.Second))
	until := clock.Now().Add(48 * time.Hour)
	cfg := "[fleet.lanes.\"" + string(laneA) + "\"]\nlane_class = \"worker-fast\"\nreversal_until = \"" +
		until.Format(time.RFC3339) + "\"\nreversal_reason = \"operator pin\"\n"
	d := runningTopology(t, cfg, clock, seedTopologyFleet(t, true))
	ctx, store := context.Background(), d.rt.Topology()
	lane, err := store.GetLane(ctx, laneA)
	want(t, err == nil && lane.LaneClass == topology.LaneClassWorkerFast, "pinned lane after the mounted pass = %+v, %v; want worker-fast", lane, err)
	rep, err := d.rt.ReconcileNow(ctx)
	want(t, err == nil && rep.RowsWritten == 0, "second pass = %+v, %v; want idempotent (0 rows)", rep, err)
	revs, err := store.ListReversals(ctx, laneA)
	want(t, err == nil && len(revs) == 1, "reversals = %+v, %v; want one audited row", revs, err)
	awaitAudit(t, d, 1)
	clock.Set(until.Add(time.Hour))
	_, err = d.rt.ReconcileNow(ctx)
	want(t, err == nil, "pass after expiry: %v", err)
	revs, err = store.ListReversals(ctx, laneA)
	want(t, err == nil && len(revs) == 1 && revs[0].RestoredAt != nil, "reversals after expiry = %+v, %v; want one restored row", revs, err)
	lane, err = store.GetLane(ctx, laneA)
	want(t, err == nil && lane.LaneClass == topology.LaneClassUnranked, "lane after expiry = %+v, %v; want the prior unranked columns", lane, err)
	rep, err = d.rt.ReconcileNow(ctx)
	want(t, err == nil && rep.RowsWritten == 0, "pass after the restore = %+v, %v; want the restore applied once", rep, err)
	awaitAudit(t, d, 2)
}

// awaitAudit waits, bounded, until the daemon bus holds n relayed audit events.
func awaitAudit(t *testing.T, d capDaemon, n int) {
	t.Helper()
	for deadline := time.Now().Add(testDaemonBound); ; time.Sleep(20 * time.Millisecond) {
		evs, err := d.bus.Replay(context.Background(), "fleet.topology.audit", 0)
		if err == nil && len(evs) == n {
			return
		}
		want(t, time.Now().Before(deadline), "daemon bus audit events = %d, %v; want %d relayed", len(evs), err, n)
	}
}
