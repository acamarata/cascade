//go:build integration

package jobs_test

// Purpose: P1-E29-W6-S60-T4 Path 2's seeding/read-back rig -- split from
//
//	acceptance_resume_integration_test.go (TestMain + daemon-lifecycle
//	helpers + the test itself) purely to keep both files under the
//	300-line cap.
//
// SPORT: jobs/acceptance/ADD (P1-E29-W6-S60-T4).

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/providers/sqlite"
)

// path2Rig bundles the real subsystems this file drives, all opened over
// {CASCADE_HOME}/data/cascade.db -- the SAME file the spawned daemon
// reads/writes, never a private throwaway db.
type path2Rig struct {
	db       *sql.DB
	store    *jobs.Store
	leases   *jobs.LeaseManager
	worktree *jobs.WorktreeManager
	ledger   *jobs.EvidenceLedger
	policy   *jobs.CompletionPolicy
}

// openPath2Rig opens (or reopens) dbPath and wires the real subsystems
// over it, on the clock given (the priming daemon spawn, not this
// helper, is what actually creates/migrates the schema -- see
// acceptance_resume_integration_test.go's header; ApplyJobsSchema et al.
// are idempotent, so calling them again here is a safe no-op re-apply,
// matching wireJobRPC/openSchedulerResumeJobsStore's own production
// contract).
//
// The caller owns closing rig.db explicitly (never t.Cleanup here): this
// rig's whole point is to prove the test process fully releases
// cascade.db before the daemon is spawned/respawned, so a lingering
// cleanup-deferred-to-test-end connection would defeat that.
func openPath2Rig(t *testing.T, dbPath string, clock runtime.Clock) *path2Rig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if err := jobs.ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := jobs.ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	if err := jobs.ApplyEvidenceSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		t.Fatalf("ApplyEvidenceSchema: %v", err)
	}
	store := jobs.NewStore(db)
	leases := jobs.NewLeaseManager(store, clock, func() bool { return true }, jobs.DefaultLeaseDefaults(), nil)
	jobs.WireLeaseRelease(store, leases)
	journalStore := journal.New(storetest.NewMemStore(), clock, journal.DefaultNamespace)
	worktree := jobs.NewWorktreeManager(store, journalStore, nil, nil)
	authz := jobs.NewProducerAuthz(store, func() bool { return true }, leases)
	writer := audit.New(storetest.NewMemStore(), clock, nil)
	ledger, err := jobs.NewEvidenceLedger(store, clock, writer, authz)
	if err != nil {
		t.Fatalf("NewEvidenceLedger: %v", err)
	}
	policy, err := jobs.NewCompletionPolicy(jobs.CompletionPolicyDeps{
		Store: store, Ledger: ledger, Clock: clock, EngineID: acceptanceEngineID,
	})
	if err != nil {
		t.Fatalf("NewCompletionPolicy: %v", err)
	}
	return &path2Rig{db: db, store: store, leases: leases, worktree: worktree, ledger: ledger, policy: policy}
}

// seedPath2Stale writes job(running) + lease(held, ALREADY STALE) + a
// real worktree + one lint EvidenceRecord + one KindCheckpoint journal
// entry -- AMD-20260923/Z1-9's exact seeding list, through the same
// package APIs acceptance_path1_test.go exercises.
//
// The lease is granted through a LeaseManager built on a
// runtime.NewFixedClock already past DefaultLeaseDefaults' TTL+grace
// threshold, so Acquire's own `IssuedAt: m.clock.Now().Unix()` (lease_
// query.go's acquireInTx) writes an ALREADY-STALE value at grant time --
// no post-hoc mutation of a granted lease's IssuedAt column. The
// production Resume path this test proves runs its own staleness check
// for real against that on-disk value.
func seedPath2Stale(t *testing.T, dbPath, repoRoot string) {
	t.Helper()
	realClock := runtime.NewSystemClock()
	staleAt := realClock.Now().Add(-(time.Duration(jobs.DefaultLeaseDefaults().TTLSeconds+jobs.DefaultLeaseDefaults().ExpiryGraceSeconds+60) * time.Second))
	seedingClock := runtime.NewFixedClock(staleAt)

	rig := openPath2Rig(t, dbPath, seedingClock)
	defer func() { _ = rig.db.Close() }()
	ctx := nodes.WithRole(context.Background(), nodes.RoleController)
	if err := rig.store.PutJob(ctx, jobs.Job{
		ID: path2JobID, State: jobs.JobStateRunning, MutableScope: "docs/**",
		RiskClass: string(jobs.RiskClassLow), MinTaskClass: "code",
		ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	}); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	acquired, err := rig.leases.Acquire(ctx, path2Repo, "docs/**", path2JobID)
	if err != nil || !acquired.Granted {
		t.Fatalf("Acquire: %+v, %v", acquired, err)
	}
	if _, err := rig.worktree.Create(ctx, acquired.Lease, repoRoot); err != nil {
		t.Fatalf("worktree Create: %v", err)
	}
	if err := rig.store.PutExecution(ctx, jobs.Execution{ID: "exec-" + path2JobID, JobID: path2JobID, Attempt: 1, State: jobs.ExecutionRunning}); err != nil {
		t.Fatalf("PutExecution: %v", err)
	}
	appendPath2Evidence(t, rig, acquired.Lease, jobs.EvidenceLint, "idem-path2-lint-1")

	driver, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open %s for journal seed: %v", dbPath, err)
	}
	defer func() { _ = driver.Close() }()
	js := journal.New(driver, seedingClock, journal.DefaultNamespace)
	if _, err := js.Append(context.Background(), path2JobID, journal.KindCheckpoint, "s60t4-seed", []byte(`{"state":"running"}`)); err != nil {
		t.Fatalf("seed journal checkpoint entry: %v", err)
	}
}

// appendPath2Evidence appends one real, controller-authorized, passing
// evidence row -- the same shape acceptance_path1_test.go's appendPass
// writes, duplicated here rather than shared because that helper closes
// over acceptanceRig, not path2Rig.
func appendPath2Evidence(t *testing.T, rig *path2Rig, lease jobs.ResourceLease, kind jobs.EvidenceKind, idem string) {
	t.Helper()
	rec := jobs.EvidenceRecord{
		JobID: path2JobID, Kind: kind, ProducerCapability: jobs.ProducerControllerRun,
		AttemptID: "exec-" + path2JobID, AttestorIdentity: "daemon:acceptance",
		Outcome: jobs.OutcomePass, IdempotencyKey: idem,
	}
	auth := jobs.AppendAuthorization{
		ExecutionID: "exec-" + path2JobID, LeaseRepoID: lease.RepoID,
		LeaseScopeGlob: lease.ScopeGlob, LeaseEpoch: lease.Epoch,
	}
	if _, err := rig.ledger.Append(context.Background(), rec, auth); err != nil {
		t.Fatalf("Append(%s): %v", kind, err)
	}
}

// assertPath2OneEvidenceRecord reopens dbPath (a short-lived read
// alongside the still-running restarted daemon -- SQLite's default
// journal mode tolerates a second connection; this rig always opens with
// SetMaxOpenConns(1) and closes immediately) and asserts the ledger
// holds exactly one record, proving Resume introduced no duplicate. This
// fact has no RPC surface (job.show returns only the Job, no evidence
// count), so it is checked directly, immediately alongside the job.show
// assertion in the same file's TestAcceptancePath2KillResume.
func assertPath2OneEvidenceRecord(t *testing.T, dbPath string) {
	t.Helper()
	rig := openPath2Rig(t, dbPath, runtime.NewSystemClock())
	defer func() { _ = rig.db.Close() }()
	if cursor, err := rig.ledger.Cursor(context.Background(), path2JobID); err != nil || cursor != 1 {
		t.Fatalf("Cursor after restart = %d, err=%v, want exactly 1 (the seeded lint record, no resume duplicate)", cursor, err)
	}
}

// clearSchedulerAdvisoryLock deletes the internal/events/scheduler
// retention-scheduler's domain-level advisory lock row
// (namespace "daemon-scheduler", key "sched:lock" -- lock.go's own
// unexported lockKey/schedulerNamespace constants, literal here since
// this test-only cross-subsystem cleanup cannot import them).
//
// WHY THIS IS NEEDED (a REAL, separate kill-9 finding, not a jobs-domain
// one): SIGKILLing the daemon never lets `startScheduler`
// (cmd/cascade/daemon_unix_scheduler.go) run its graceful
// `advisoryLock.Release`, so the lock row survives with a live 5-minute
// TTL (`schedulerLeaseTTL`) stamped to a DIFFERENT, now-dead owner id.
// Every subsequent `cascade daemon run` -- including the restart this
// test needs for its ACTUAL Resume assertion -- refuses to start at all
// (`sched.Activate` fails fatally, verified against the real binary:
// `error: conflict: scheduler: advisory lock held by "<dead-owner>" until
// <+5m>`) until that TTL naturally elapses, which a real test cannot
// wait out. Clearing the stale row (the operationally correct recovery a
// real operator would perform, per lock.go's own "a lease that is not
// renewed before it expires becomes stealable" design) is this test's
// only path to actually exercising the restart's jobs-domain Resume at
// all.
func clearSchedulerAdvisoryLock(t *testing.T, dbPath string) {
	t.Helper()
	driver, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("open %s for advisory-lock clear: %v", dbPath, err)
	}
	defer func() { _ = driver.Close() }()
	if err := driver.Delete(context.Background(), "daemon-scheduler", "sched:lock"); err != nil {
		t.Fatalf("clear stale scheduler advisory lock: %v", err)
	}
}

// driveP2ToAccepted opens a fresh rig -- the daemon has been SIGTERM'd by
// this point, so there is no concurrent writer on cascade.db -- and
// drives the RESUMED job leased->running->verifying->reviewing->accepted
// directly against the store/ledger/policy. This is the SAME "no
// persisted per-repo DAG coordinator exists yet" pattern
// acceptance_path1_test.go's runCompletionToAccepted already establishes
// (subsystems_scheduler.go's own disclosed CONTRACT DEVIATION), never
// routed back through the now-dead daemon.
func driveP2ToAccepted(t *testing.T, dbPath string) {
	t.Helper()
	rig := openPath2Rig(t, dbPath, runtime.NewSystemClock())
	defer func() { _ = rig.db.Close() }()
	ctx := nodes.WithRole(context.Background(), nodes.RoleController)

	lease, ok, err := rig.store.GetLease(ctx, path2Repo, "docs/**")
	if err != nil || !ok {
		t.Fatalf("GetLease after resume: ok=%v err=%v", ok, err)
	}
	if err := rig.store.PutTransition(ctx, path2JobID, jobs.JobStateRunning, 2); err != nil {
		t.Fatalf("PutTransition leased->running: %v", err)
	}
	appendPath2Evidence(t, rig, lease, jobs.EvidenceTests, "idem-path2-tests-1")

	job, ok, err := rig.store.GetJob(ctx, path2JobID)
	if err != nil || !ok {
		t.Fatalf("GetJob: ok=%v err=%v", ok, err)
	}
	req := func(target jobs.JobState) jobs.TransitionRequest {
		return jobs.TransitionRequest{
			Job: &job, Target: target, Caller: jobs.PolicyEngineIdentity{EngineID: acceptanceEngineID},
			PlannedRiskClass:   jobs.RiskClassLow,
			ActualFootprint:    jobs.ChangeFootprint{ChangedPaths: []string{"docs/fixture-a.md", "docs/fixture-b.md"}},
			LeaseScopePrefixes: []string{"docs/"},
		}
	}
	if err := rig.policy.Transition(ctx, req(jobs.JobStateVerifying)); err != nil {
		t.Fatalf("Transition ->verifying: %v", err)
	}
	if err := rig.policy.Transition(ctx, req(jobs.JobStateReviewing)); err != nil {
		t.Fatalf("Transition ->reviewing: %v", err)
	}
	appendPath2Evidence(t, rig, lease, jobs.EvidenceReview, "idem-path2-review-1")
	if err := rig.policy.Transition(ctx, req(jobs.JobStateAccepted)); err != nil {
		t.Fatalf("Transition ->accepted: %v", err)
	}
	assertP2Accepted(ctx, t, rig)
}

// assertP2Accepted is driveP2ToAccepted's final-state assertion block
// (Art.10.3 funlen split, no new concern): job.State == accepted, all
// three evidence kinds pass, and the ledger's chain cursor -- the total
// row count, an append-only seq with no gaps -- is exactly 3, proving
// resume introduced no duplicate.
func assertP2Accepted(ctx context.Context, t *testing.T, rig *path2Rig) {
	t.Helper()
	final, ok, err := rig.store.GetJob(ctx, path2JobID)
	if err != nil || !ok || final.State != jobs.JobStateAccepted {
		t.Fatalf("final job state = %+v, ok=%v, err=%v, want accepted", final, ok, err)
	}
	for _, kind := range []jobs.EvidenceKind{jobs.EvidenceLint, jobs.EvidenceTests, jobs.EvidenceReview} {
		rec, err := rig.ledger.Query(ctx, path2JobID, kind)
		if err != nil || rec.Outcome != jobs.OutcomePass {
			t.Fatalf("Query(%s) = %+v, err=%v, want outcome=pass", kind, rec, err)
		}
	}
	if cursor, err := rig.ledger.Cursor(ctx, path2JobID); err != nil || cursor != 3 {
		t.Fatalf("Cursor = %d, err=%v, want exactly 3 (no duplicates introduced by the resume)", cursor, err)
	}
}
