package daemon

// Purpose: the reservation step composes one handle, one jobs store, one
//
//	LeaseManager, a fenced WorktreeManager and one Reserver bound to the
//	production seams, and Close releases the handle once, after the loop.
//
// SPORT: internal/daemon (ADD, P1-CAP-10).

import (
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/runtime"
)

// stubLeaseProbe reports every holder process group as alive or not.
type stubLeaseProbe struct{ alive atomic.Bool }

func (p *stubLeaseProbe) IsAlive(int64) bool { return p.alive.Load() }

// resRig is a started fleet runtime with a fake liveness ticker and probe.
type resRig struct {
	*fleetRig
	rt    *FleetRuntime
	live  *fleetFakeTicker
	probe *stubLeaseProbe
}

func newResRig(t *testing.T) *resRig {
	t.Helper()
	g := newFleetRig(t, "")
	live, probe := newFleetFakeTicker(), &stubLeaseProbe{}
	savedTicker, savedProbe := newLivenessTicker, newLeaseProbe
	newLivenessTicker = func(time.Duration) runtime.Ticker { return live }
	newLeaseProbe = func() jobs.ProcessLivenessProbe { return probe }
	t.Cleanup(func() { newLivenessTicker, newLeaseProbe = savedTicker, savedProbe })
	return &resRig{fleetRig: g, rt: g.start(t), live: live, probe: probe}
}

// startLoop runs the start sweep (no other daemon) and waits for the loop.
func (g *resRig) startLoop(t *testing.T) {
	t.Helper()
	if err := g.rt.StartReservations(g.ctx, fakeProber{}); err != nil {
		t.Fatalf("StartReservations: %v", err)
	}
	g.live.awaitPoll(t)
}

// subsystemRow returns name's manifest row.
func subsystemRow(m *Manifest, name string) SubsystemStatus {
	for _, s := range m.Snapshot() {
		if s.Name == name {
			return s
		}
	}
	return SubsystemStatus{}
}

// TestFleetRuntimeSharesOneJobsDB: every schema is on the one handle the
// jobs store, ReservationStore and QuotaStore share.
func TestFleetRuntimeSharesOneJobsDB(t *testing.T) {
	g := newResRig(t)
	db := g.rt.JobsDB()
	if g.rt.res.resStore.DB() != db || db.Stats().MaxOpenConnections != 1 {
		t.Fatalf("ReservationStore.DB() = %p (max %d), want the runtime handle %p with MaxOpenConns 1", g.rt.res.resStore.DB(), db.Stats().MaxOpenConnections, db)
	}
	for _, table := range []string{"jobs_reservation", "sessions_quota_bucket", "jobs_resource_lease", "jobs_outbox"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s on the runtime handle: n=%d err=%v", table, n, err)
		}
	}
	got, err := g.rt.Leases().Acquire(g.ctx, "repo-one", "o/**", "job-one")
	if err != nil || !got.Granted {
		t.Fatalf("Acquire = %+v, %v", got, err)
	}
	var holder string
	if err := db.QueryRow(`SELECT holder FROM jobs_resource_lease WHERE repo_id='repo-one'`).Scan(&holder); err != nil || holder != "job-one" {
		t.Fatalf("lease read through JobsDB = %q, %v; want the jobs store's row", holder, err)
	}
}

// TestFleetRuntimeWorktreeManagerFenced: the orphan sweep ran once and the
// manager refuses a Create under a released lease (the runtime fence).
func TestFleetRuntimeWorktreeManagerFenced(t *testing.T) {
	g := newResRig(t)
	if row := subsystemRow(g.deps.Manifest, worktreeSweepSubsystem); row.State != SubsystemRunning {
		t.Fatalf("worktree sweep row = %+v, want running (swept once at build)", row)
	}
	got, err := g.rt.Leases().Acquire(g.ctx, "repo-wt", "w/**", "job-wt")
	if err != nil || !got.Granted {
		t.Fatalf("Acquire = %+v, %v", got, err)
	}
	if err := g.rt.Leases().Release(g.ctx, "repo-wt", got.Lease.ScopeGlob, got.Lease.Epoch); err != nil {
		t.Fatalf("Release: %v", err)
	}
	_, err = g.rt.Worktrees().Create(g.ctx, got.Lease, t.TempDir())
	if !hasIdentity(err, jobs.ErrLeaseFenced) {
		t.Fatalf("Create under a released lease = %v, want ErrLeaseFenced", err)
	}
}

// hasIdentity walks err's chain for target itself.
func hasIdentity(err, target error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err == target {
			return true
		}
	}
	return false
}

// TestFleetRuntimeCloseOrder: Close after Manifest.Wait closes the handle
// exactly once (a second Close changes nothing) and the loop is joined.
func TestFleetRuntimeCloseOrder(t *testing.T) {
	var closes atomic.Int32
	saved := closeJobsDB
	closeJobsDB = func(db *sql.DB) error { closes.Add(1); return db.Close() }
	t.Cleanup(func() { closeJobsDB = saved })
	g := newResRig(t)
	g.startLoop(t)
	g.cancel()
	g.awaitJoin(t)
	if row := subsystemRow(g.deps.Manifest, fleetLivenessSubsystem); row.State != SubsystemSkipped {
		t.Fatalf("liveness row after the join = %+v, want stopped", row)
	}
	if closes.Load() != 0 {
		t.Fatal("the handle closed before Close")
	}
	_ = g.rt.Close()
	_ = g.rt.Close()
	if n := closes.Load(); n != 1 {
		t.Fatalf("handle closed %d times, want exactly once", n)
	}
	if err := g.rt.JobsDB().Ping(); err == nil {
		t.Fatal("JobsDB().Ping after Close succeeded, want a closed handle")
	}
}

// TestFleetRuntimeReserverSeams: the one Reserver drives the production
// seams end to end: quota from the topology store on the runtime handle,
// leases through the runtime LeaseManager, a real worktree through the
// fenced WorktreeManager, and Release tears all of it down.
func TestFleetRuntimeReserverSeams(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the reservation runtime serves only the !windows daemon; worktrees need posix repo paths")
	}
	g := newResRig(t)
	repo := seedReserveTopology(t, g)
	req := economics.ReserveRequest{ExecutionID: "exec-seams", JobID: "job-seams", ProjectID: "project-1", LaneID: "lane-1",
		DomainID: "dom-seams", RepoID: repo, Kind: economics.ReservationInteractive, BasePrice: 1,
		Estimate: economics.Estimate{TokensIn: 10, TokensOut: 10, Requests: 1}, ScopeGlobs: []string{"src/**"}}
	r, err := g.rt.Reserver().Reserve(g.ctx, req)
	if err != nil || r.State != economics.ReservationHeld || len(r.LeaseIDs) != 1 || r.WorktreeID == "" {
		t.Fatalf("Reserve = %+v, %v; want held with one lease and a worktree", r, err)
	}
	lease, _, _ := g.rt.Jobs().GetLease(g.ctx, repo, "src/**")
	if lease.Holder != "job-seams" || r.LeaseIDs[0] != leaseHandle(lease) {
		t.Fatalf("lease = %+v, handle %q; want held by the job at the handle's epoch", lease, r.LeaseIDs[0])
	}
	if path, found, err := g.rt.res.findWorktree(g.ctx, r.LeaseIDs[0]); err != nil || !found || path != r.WorktreeID {
		t.Fatalf("findWorktree = %q %v %v, want %q", path, found, err, r.WorktreeID)
	}
	if _, found, err := g.rt.res.findWorktree(g.ctx, jobs.LeaseID(lease)+"@"+strconv.FormatInt(lease.Epoch+1, 10)); err != nil || found {
		t.Fatalf("findWorktree(superseded handle) found=%v err=%v, want not found", found, err)
	}
	if valid, err := g.rt.res.validateLeases(g.ctx, "job-seams", r.LeaseIDs); err != nil || len(valid) != 1 {
		t.Fatalf("validateLeases = %v, %v; want the one held lease", valid, err)
	}
	if ids, err := g.rt.res.findLeases(g.ctx, repo, "job-seams", []string{"src/**"}); err != nil || len(ids) != 1 || ids[0] != r.LeaseIDs[0] {
		t.Fatalf("findLeases = %v, %v; want %v", ids, err, r.LeaseIDs)
	}
	if _, err := g.rt.Reserver().Release(g.ctx, r.ID); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if l, _, _ := g.rt.Jobs().GetLease(g.ctx, repo, "src/**"); l.State != jobs.LeaseReleased {
		t.Fatalf("lease after Release = %+v, want released", l)
	}
	if _, found, _ := g.rt.Jobs().WorktreeForLease(g.ctx, repo, "src/**"); found {
		t.Fatal("worktree row survived Release")
	}
	assertOneProductionLine(t, "economics.NewReserver(", "internal/daemon/subsystems_fleet_reservation.go")
}

// seedReserveTopology writes an api_project domain with roomy buckets on the
// runtime handle and returns a real git repository (the repo id).
func seedReserveTopology(t *testing.T, g *resRig) string {
	t.Helper()
	store := topology.NewStore(g.rt.JobsDB())
	acct := topology.Account{ID: "acct-seams", Provider: "prov-a", Billing: topology.BillingInfo{Kind: topology.BillingAPI}, Role: topology.AccountRoleWorkforce}
	if err := store.UpsertAccount(g.ctx, acct); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}
	dom := topology.QuotaDomain{ID: "dom-seams", AccountRef: acct.ID, Kind: topology.QuotaDomainAPIProject, BillingTier: topology.BillingTierPaid}
	if err := store.UpsertQuotaDomain(g.ctx, dom); err != nil {
		t.Fatalf("UpsertQuotaDomain: %v", err)
	}
	for _, dim := range []string{topology.DimensionRPM, topology.DimensionTPM, topology.DimensionRPD} {
		b := topology.Bucket{Name: dim, Limit: 1_000_000, RemainingFraction: 1, Window: topology.BucketWindowDay,
			Source: topology.SourceProviderStatus, Confidence: 1, LimitScopeID: topology.ResolveScope(acct, dom),
			CapacityObserved: 1_000_000, WindowID: "w1", Version: 1, ObservedAt: g.clock.Now()}
		if err := g.rt.Quota().UpsertBucket(g.ctx, dom.ID, dim, b); err != nil {
			t.Fatalf("UpsertBucket(%s): %v", dim, err)
		}
	}
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=r@example.invalid", "-c", "user.name=r", "commit", "-q", "--allow-empty", "-m", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return root
}

// assertOneProductionLine: git grep -F pattern over non-test Go files
// prints exactly one line, in file.
func assertOneProductionLine(t *testing.T, pattern, file string) {
	t.Helper()
	cmd := exec.Command("git", "grep", "-n", "-F", pattern, "--", "*.go", ":!*_test.go")
	cmd.Dir = filepath.Join("..", "..")
	out, err := cmd.Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err != nil || len(lines) != 1 || !strings.HasPrefix(lines[0], file+":") {
		t.Fatalf("git grep %q = %q, %v; want one line in %s", pattern, lines, err, file)
	}
}
