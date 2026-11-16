//go:build !windows

// Purpose: the fleet-dispatch binding (wire_fleet_dispatch.go) over a composed
//   daemon with a funded local lane, and a wiring without a fleet runtime.

package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/jobs"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

var (
	localDispatchSel = provider.Selection{LaneID: "local-llm-1"}
	errTestCount     = cascade.New(cascade.KindInternal, "test: token counter failed")
)

type failingCounter struct{}

func (failingCounter) Count(context.Context, string) (int, error) { return 0, errTestCount }

type countingResolver struct{ n atomic.Int32 }

func (r *countingResolver) Resolve(context.Context, provider.Selection) (provider.ModelProvider, error) {
	r.n.Add(1)
	return nil, cascade.New(cascade.KindInvalidInput, "test: no model adapter")
}

func dispatchRig(t *testing.T) (capDaemon, *fleetDispatch) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home) // composeForTest sets HOME
	d := composeCapacity(t)
	seedLocalProvider(t, d)
	reconcileAndFund(t, d, "local-llm:api")
	a := dispatchSlotFor(d.c.Opts.Manifest).bound.Load()
	want(t, a != nil, "the composed daemon bound no fleet dispatch adapter")
	return d, a
}

// dispatchReq has one input per size, each of that many runes.
func dispatchReq(task, class string, sizes ...int) provider.ModelRequest {
	req := provider.ModelRequest{TaskID: task, TaskClass: class, Sensitivity: provider.SensitivityPublic}
	for i, n := range sizes {
		req.Inputs = append(req.Inputs, provider.ChatMessage{Role: "user", Content: strings.Repeat(string(rune('a'+i)), n)})
	}
	return req
}

func holdOf(t *testing.T, a *fleetDispatch, req provider.ModelRequest) conductor.HeldCall {
	t.Helper()
	h, err := a.Reserve(context.Background(), localDispatchSel, req)
	want(t, err == nil && h != nil, "Reserve(%s) = %v", req.TaskID, err)
	return h
}

func TestDispatchEstimateUsesEstimateFor(t *testing.T) {
	d, a := dispatchRig(t)
	ctx, seen := context.Background(), map[economics.Estimate]bool{}
	for _, class := range []conductor.TaskClass{conductor.TaskClassChat, conductor.TaskClassClassify} {
		for _, sizes := range [][]int{{40}, {400, 1203}} {
			req := dispatchReq("job-est", string(class), sizes...)
			var n int64
			for _, m := range req.Inputs {
				c, _ := provider.NaiveTokenCounter{}.Count(ctx, m.Content)
				n += int64(c)
			}
			wantEst, eerr := economics.EstimateFor(n, class, 1)
			h := holdOf(t, a, req)
			row, found, err := a.rows.Get(ctx, h.ID())
			want(t, eerr == nil && err == nil && found && row.Estimate == wantEst, "row %+v (%v %v %v), want Estimate %+v", row, found, err, eerr, wantEst)
			seen[row.Estimate] = true
			want(t, h.Release(ctx, provider.Usage{}, false) == nil, "Release")
		}
	}
	want(t, len(seen) == 4, "estimates %v, want four distinct", seen)
	before := rowCount(t, d, "jobs_reservation")
	_, err := a.Reserve(ctx, localDispatchSel, dispatchReq("job-bad", "not-a-class", 40))
	want(t, hasLeaseSentinel(err, economics.ErrUnknownTaskClass) && strings.Contains(err.Error(), "unknown task class"),
		"unknown class Reserve = %v, want ErrUnknownTaskClass", err)
	want(t, rowCount(t, d, "jobs_reservation") == before, "an unknown class wrote a row")
	assertExecutorCountRefusal(t, d, a)
}

// assertExecutorCountRefusal: a failing counter refuses with no adapter call
// and no row; the real counter reaches the adapter once and writes one row.
func assertExecutorCountRefusal(t *testing.T, d capDaemon, a *fleetDispatch) {
	t.Helper()
	ctx, req := context.Background(), dispatchReq("job-exec", "chat", 40)
	before := rowCount(t, d, "jobs_reservation")
	bad := *a
	bad.counter = failingCounter{}
	res := dispatchExecutor(t, d, &bad)
	_, err := daemon.ProcessConductorDoor().Execute(ctx, req)
	want(t, hasLeaseSentinel(err, errTestCount) && strings.Contains(err.Error(), "counting input tokens"),
		"Execute with a failing counter = %v, want the counter error", err)
	want(t, res.n.Load() == 0 && rowCount(t, d, "jobs_reservation") == before, "refusal made %d adapter calls, wrote rows", res.n.Load())
	awaitLane(t, d, "local-llm-1")
	res = dispatchExecutor(t, d, a)
	_, err = daemon.ProcessConductorDoor().Execute(ctx, req)
	want(t, err != nil && res.n.Load() == 1 && rowCount(t, d, "jobs_reservation") == before+1,
		"real counter: Execute = %v after %d adapter calls, want one call and one row", err, res.n.Load())
}

// dispatchExecutor registers conductor.execute over r and a counting
// resolver, binding the process door to that Executor.
func dispatchExecutor(t *testing.T, d capDaemon, r conductor.CallReserver) *countingResolver {
	t.Helper()
	resetProcessFirewall(t)
	paths, clock, cfg := fakeMemoryPaths{root: t.TempDir()}, runtime.NewSystemClock(), &runtime.Config{}
	want(t, os.MkdirAll(paths.DataDir(), 0o700) == nil, "mkdir data dir")
	security, err := conductorSecurity(paths)
	policy, perr := buildConductorPolicy(func() *runtime.Config { return cfg }, clock)
	want(t, err == nil && perr == nil, "conductorSecurity: %v; buildConductorPolicy: %v", err, perr)
	security.Policy = conductor.NewOwnerPolicy(policy.Deny)
	store, res := storetest.NewMemStore(), &countingResolver{}
	err = daemon.RegisterConductorExecuteHandler(rpc.NewRegistry(), daemon.NewManifest(nil, clock), providerregistry.NewReader(d.parts.Registry),
		policy.Quota, res, audit.New(store, clock, nil), clock, security, daemon.ConductorAccounting{Reserver: r},
		daemon.NewConductorFanOut(store, clock), conductor.WithLaneTierCap(func(context.Context, conductor.LaneID, conductor.LaneType) (provider.SensitivityTier, string, error) {
			return provider.SensitivityLocalOnly, "test-fixture", nil
		}))
	want(t, err == nil, "RegisterConductorExecuteHandler: %v", err)
	return res
}

// schedulerRow is m's jobs.scheduler row.
func schedulerRow(m *daemon.Manifest) daemon.SubsystemStatus {
	snap := m.Snapshot()
	if i := slices.IndexFunc(snap, func(s daemon.SubsystemStatus) bool { return s.Name == "jobs.scheduler" }); i >= 0 {
		return snap[i]
	}
	return daemon.SubsystemStatus{}
}

func TestDaemonBindsReserver(t *testing.T) {
	d, a := dispatchRig(t)
	want(t, a.reserver != nil && a.reserver == d.rt.Reserver(), "adapter reserver %p, want FleetRuntime.Reserver %p", a.reserver, d.rt.Reserver())
	want(t, schedulerRow(d.c.Opts.Manifest).State == daemon.SubsystemRunning, "scheduler with a fleet = %+v", schedulerRow(d.c.Opts.Manifest))
	paths, clock, store := fakeMemoryPaths{root: t.TempDir()}, runtime.NewFixedClock(fixedSchedulerResumeE2ENow), storetest.NewMemStore()
	seedStaleRunningJobDB(t, paths, "job-no-fleet")
	_, jerr := journal.New(store, clock, journal.DefaultNamespace).Append(context.Background(), "job-no-fleet", journal.KindIntent, "seed", []byte(`{}`))
	_, m, _, err := buildRPCServer(events.New(store, clock), clock, nil, daemon.Settings{SocketPath: filepath.Join(paths.root, "d.sock")}, paths, nil, store)
	want(t, jerr == nil && err == nil, "seed journal: %v; buildRPCServer without a fleet: %v", jerr, err)
	row := schedulerRow(m)
	want(t, row.State == daemon.SubsystemRunning && strings.Contains(row.Detail, "1 left waiting"), "scheduler without a fleet = %+v", row)
	want(t, getJobStateFromDB(t, paths, "job-no-fleet") == jobs.JobStateLeased, "the leased job did not wait at leased")
	_, err = dispatchSlotFor(m).Reserve(context.Background(), localDispatchSel, dispatchReq("job-x", "chat", 4))
	want(t, hasLeaseSentinel(err, conductor.ErrUnreservedDispatch) && strings.Contains(err.Error(), "no fleet reservation runtime"),
		"dispatch without a fleet = %v, want ErrUnreservedDispatch", err)
}

func TestReservationReusesFleetManagers(t *testing.T) {
	d, a := dispatchRig(t)
	ctx := context.Background()
	want(t, d.parts.Deps.Reserver == d.rt.Reserver() && a.reserver == d.parts.Deps.Reserver, "the dispatch and lease doors hold different Reservers")
	want(t, a.rows != nil && any(a.rows) == any(d.parts.Deps.Reservations) && a.projects.count == d.rt.Projects() &&
		d.rt.Leases() != nil && d.rt.Worktrees() != nil, "the adapter reads another reservation store or project counter")
	h := holdOf(t, a, dispatchReq("job-reuse", "code", 80))
	row, _, err := a.rows.Get(ctx, h.ID())
	want(t, err == nil && len(row.LeaseIDs) == 0 && row.WorktreeID == "", "dispatch row %+v (%v), want quota-only", row, err)
	byExec, found, err := d.rt.Fleet().ReservationByExecution(ctx, row.ExecutionID)
	want(t, err == nil && found && byExec.ID == h.ID(), "Fleet ledger lookup = %+v %v %v", byExec, found, err)
	want(t, h.Release(ctx, provider.Usage{InputTokens: 3, OutputTokens: 5}, true) == nil, "Release")
	row, _, _ = a.rows.Get(ctx, h.ID())
	want(t, row.State == economics.ReservationReleased && row.ActualSource == economics.ActualSourceReported && row.Actual.TokensOut == 5,
		"released row %+v, want released with the reported usage", row)
}

func TestTopFourConstructsNoSecondInstance(t *testing.T) {
	root := moduleRootForTest(t)
	allowed := map[string]string{"economics.NewReserver": "internal/daemon/subsystems_fleet_reservation.go",
		"capacity.NewFleet": "internal/daemon/subsystems_fleet_capacity.go", "governor.NewAdmissionController": "internal/daemon/subsystems_fleet.go"}
	found := map[string][]string{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case de.IsDir() && (strings.HasPrefix(de.Name(), ".") || de.Name() == "testdata"):
			return filepath.SkipDir
		case de.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go"):
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, p)
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && allowed[exprText(sel)] != "" {
				found[exprText(sel)] = append(found[exprText(sel)], filepath.ToSlash(rel))
			}
			return true
		})
		return nil
	})
	want(t, err == nil, "walk: %v", err)
	for k, file := range allowed {
		want(t, slices.Equal(found[k], []string{file}), "%s called from %v, want only %s", k, found[k], file)
	}
	slots := registrationSlots(t, "wire_fleet_dispatch.go", "daemon_unix_conductor.go")
	want(t, slices.Equal(slots, []string{"fleet-dispatch phaseConductor 60"}), "this ticket's registrations = %v", slots)
}

// registrationSlots renders every daemonRegistration literal in files as
// "Name Phase Order".
func registrationSlots(t *testing.T, files ...string) []string {
	t.Helper()
	var out []string
	for _, name := range files {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		want(t, err == nil, "parse %s: %v", name, err)
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok && exprText(lit.Type) == "daemonRegistration" {
				vals := map[string]string{}
				for _, e := range lit.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok {
						vals[exprText(kv.Key)] = strings.Trim(exprText(kv.Value), `"`)
					}
				}
				out = append(out, vals["Name"]+" "+vals["Phase"]+" "+vals["Order"])
			}
			return true
		})
	}
	return out
}

func TestProjectCountFollowsLiveReservations(t *testing.T) {
	d, a := dispatchRig(t)
	ctx, count := context.Background(), d.rt.Projects()
	setRPDCapacity(t, d, 3)
	a1 := holdOf(t, a, dispatchReq("job-a", "chat", 8))
	want(t, count.Count() == 1, "count after the first project = %d", count.Count())
	b1 := holdOf(t, a, dispatchReq("job-b", "chat", 8))
	want(t, count.Count() == 2, "count with two projects = %d", count.Count())
	_, err := a.Reserve(ctx, localDispatchSel, dispatchReq("job-a", "chat", 8))
	want(t, hasLeaseSentinel(err, economics.ErrProjectShareExceeded) && strings.Contains(err.Error(), "(2 active projects)"),
		"second row of job-a beside job-b = %v, want the share refused over 2 projects", err)
	want(t, count.Count() == 2 && b1.Release(ctx, provider.Usage{}, false) == nil && count.Count() == 1, "count after job-b stopped = %d", count.Count())
	a2 := holdOf(t, a, dispatchReq("job-a", "chat", 8))
	want(t, count.Count() == 1 && a1.Release(ctx, provider.Usage{}, false) == nil && count.Count() == 1, "count with two job-a rows, then one = %d", count.Count())
	want(t, a2.Release(ctx, provider.Usage{}, false) == nil && count.Count() == 0, "count after the last terminal row = %d", count.Count())
}

// setRPDCapacity replaces the local domain's barrier bucket with capacity n.
func setRPDCapacity(t *testing.T, d capDaemon, n int64) {
	t.Helper()
	ctx, store := context.Background(), topology.NewStore(d.rt.JobsDB())
	dom, err := store.GetQuotaDomain(ctx, "local-llm:api")
	acct, aerr := store.GetAccount(ctx, dom.AccountRef)
	want(t, err == nil && aerr == nil, "GetQuotaDomain: %v; GetAccount: %v", err, aerr)
	b := topology.Bucket{Name: topology.DimensionRPD, Limit: n, RemainingFraction: 1, Window: topology.BucketWindowDay,
		Source: topology.SourceProviderStatus, Confidence: 1, LimitScopeID: topology.ResolveScope(acct, dom),
		CapacityObserved: n, WindowID: "w2", Version: 2, ObservedAt: time.Now()}
	want(t, d.rt.Quota().UpsertBucket(ctx, dom.ID, topology.DimensionRPD, b) == nil, "UpsertBucket(rpd)")
}

func TestCIIntentsLeaseThroughBoundReserver(t *testing.T) {
	d, a := dispatchRig(t)
	awaitLane(t, d, "local-llm-1")
	ctx, public := context.Background(), provider.SensitivityPublic
	j := jobs.Job{ID: "job-ci", State: jobs.JobStatePending, CreatedAt: 1, UpdatedAt: 1, RiskClass: string(jobs.RiskClassLow),
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: &public}
	want(t, d.rt.Jobs().PutJob(ctx, j) == nil, "PutJob")
	req := capacity.ResourceRequest{Intent: "ci:test", TaskClass: conductor.TaskClassCode, MinQuality: capacity.QualityStandard,
		Deadline: capacity.DeadlineInteractive, Sensitivity: public, RiskClass: jobs.RiskClassLow, DataClass: &public}
	lease, lerr := d.rt.Fleet().Lease(ctx, req, capacity.DispatchRef{JobID: "job-ci", ProjectID: "project-ci", HydratedTokens: 100, Requests: 1}, 1)
	row, found, err := a.rows.Get(ctx, lease.ReservationID)
	want(t, lerr == nil && err == nil && found && row.State == economics.ReservationHeld && row.ProjectID == "project-ci", "ci lease %v; row %+v (%v %v)", lerr, row, found, err)
	want(t, d.parts.Deps.Reserver == a.reserver && d.rt.Projects().Count() == 0, "the ci lease used another Reserver or the model dispatch adapter")
	want(t, d.rt.Fleet().Release(ctx, lease) == nil, "Release")
}
