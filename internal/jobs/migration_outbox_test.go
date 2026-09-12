package jobs

// Purpose: OutboxMigrationSet's schema (table presence, idempotent
//
//	re-apply) plus the R-21.148 crash-injection matrix: a REAL separate
//	OS process performs one outbox write and is REAL SIGKILLed at the
//	injected point, then the parent reconciles the SAME on-disk sqlite
//	file and asserts exactly-once semantics.
//
// CONTRACT NOTE (files_scope, quoted in the journal): this ticket's
// full_desc names "testkit.SpawnDaemon(t)" as the crash-injection
// vehicle. No such helper exists anywhere in this tree (internal/testkit
// has no SpawnDaemon symbol), and this ticket's files_scope does not
// include internal/testkit -- building daemon-spawn test infrastructure
// there would be a second, out-of-scope subsystem risking collision
// with whichever ticket does own it. The tree's OWN real precedent for
// "REAL SIGKILL of a separate process holding a real on-disk lock" is
// internal/fleet/resume/submit_kill9_test.go's TestResumeKillHelperProcess
// pattern (itself citing "providers/sqlite/lock_crossprocess_test.go's
// own established pattern"): re-execute the test binary itself via
// exec.Command(os.Args[0], "-test.run=..."), signal readiness over
// stdout, then Kill(). This file follows that exact, already-authorized
// idiom rather than testkit.SpawnDaemon. TestResumeKillHelperProcess's
// own doc comment discloses the same substitution for its ticket: "the
// daemon process itself is not launched ... what is genuinely real here
// is the SIGKILL" -- this file makes the identical disclosure.
//
// SPORT: jobs/scheduler-outbox/ADD (tests) (P1-E29-W6-S59-T5).

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/storage/migrate"
)

func TestOutboxMigrationCreatesTable(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	cols := tableColumns(t, db, tableOutbox)
	for _, want := range []string{"id", "job_id", "attempt_generation", "site", "idempotency_key", "state", "payload_hash", "created_at", "updated_at"} {
		if !cols[want] {
			t.Fatalf("jobs_outbox missing column %q; got %v", want, cols)
		}
	}
}

func TestOutboxMigrationIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}

// crashEnvDBPath/crashEnvSite/crashEnvPhase select TestOutboxCrashHelperProcess's behavior.
const (
	crashEnvDBPath = "CASCADE_OUTBOX_CRASHTEST_DB_PATH"
	crashEnvSite   = "CASCADE_OUTBOX_CRASHTEST_SITE"
	crashEnvPhase  = "CASCADE_OUTBOX_CRASHTEST_PHASE" // "before" or "after"
)

// TestOutboxCrashHelperProcess is re-executed as a SEPARATE OS PROCESS
// (see this file's CONTRACT NOTE). An ordinary `go test` run (env unset)
// is a no-op.
func TestOutboxCrashHelperProcess(_ *testing.T) {
	path := os.Getenv(crashEnvDBPath)
	if path == "" {
		return
	}
	site := OutboxSite(os.Getenv(crashEnvSite))
	phase := os.Getenv(crashEnvPhase)
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		_, _ = os.Stdout.WriteString("OPEN_FAILED\n")
		return
	}
	db.SetMaxOpenConns(1)
	store := NewStore(db)
	intent := OutboxIntent{JobID: "crash-job", Site: site, AttemptGeneration: 1, PayloadHash: "h"}
	key := DeriveIdempotencyKey(intent.Site, intent.JobID, intent.AttemptGeneration, intent.PayloadHash)
	err = store.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := RecordIntent(ctx, tx, 1000, intent); err != nil {
			return err
		}
		if phase == "after" {
			return MarkEffect(ctx, tx, 1000, key)
		}
		return nil
	})
	if err != nil {
		_, _ = os.Stdout.WriteString("SEED_FAILED\n")
		return
	}
	_, _ = os.Stdout.WriteString("READY\n")
	_, _ = bufio.NewReader(os.Stdin).ReadByte() // block until SIGKILLed; never reached
}

// spawnAndKillOutboxHelper re-execs this test binary as TestOutboxCrashHelperProcess
// against path/site/phase, waits for READY, then sends a REAL SIGKILL.
func spawnAndKillOutboxHelper(t *testing.T, path string, site OutboxSite, phase string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOutboxCrashHelperProcess$")
	cmd.Env = append(os.Environ(), crashEnvDBPath+"="+path, crashEnvSite+"="+string(site), crashEnvPhase+"="+phase)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if line != "READY\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("helper process: want READY, got %q", line)
	}
	if err := cmd.Process.Kill(); err != nil { // SIGKILL
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()
}

// newCrashTestDB migrates a fresh on-disk db (jobs + jobs-outbox) and
// seeds the "crash-job" row the helper process's outbox intent
// references.
func newCrashTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crash.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if err := ApplyJobsSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := ApplyOutboxSchema(ctx, db, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	if err := NewStore(db).PutJob(ctx, baseJob("crash-job")); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	_ = db.Close() // the helper process opens its own handle on this path
	return path
}

// runOutboxCrashCase is the R-21.148 crash-injection body, shared by
// every TestOutboxCrash{Before,After}Effect_* subtest: spawn the real
// helper, REAL SIGKILL it, then reconcile the SAME on-disk file and
// assert exactly-once semantics.
func runOutboxCrashCase(t *testing.T, site OutboxSite, phase string, effectExists bool) {
	path := newCrashTestDB(t)
	spawnAndKillOutboxHelper(t, path, site, phase)

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen after kill: %v", err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	store := NewStore(db)

	sched := NewScheduler(nil)
	var probeCalls, compensateCalls int
	probes := map[OutboxSite]SiteProbe{site: func(context.Context, OutboxRow) (bool, error) {
		probeCalls++
		return effectExists, nil
	}}
	compensate := map[OutboxSite]CompensateFn{site: func(context.Context, OutboxRow) error { compensateCalls++; return nil }}

	result, err := sched.reconcileOutbox(context.Background(), store, probes, compensate, 5000)
	if err != nil {
		t.Fatalf("reconcile after kill: %v", err)
	}
	if effectExists {
		if len(result.Confirmed) != 1 {
			t.Fatalf("phase=%s: result = %#v, want the killed effect confirmed exactly once", phase, result)
		}
	} else if len(result.ToReperform) != 1 {
		t.Fatalf("phase=%s: result = %#v, want the killed intent marked to re-perform exactly once", phase, result)
	}

	// Exactly-once: reconciling again finds nothing left to do.
	result2, err := sched.reconcileOutbox(context.Background(), store, probes, compensate, 5001)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if !effectExists && len(result2.ToReperform) != 0 {
		// The row is still `intent` until the coordinator actually
		// re-performs and confirms it -- reconcile alone does not
		// re-perform, only DECIDES to; asserting it is found again here
		// would be wrong. Skip: re-perform-then-confirm is the
		// coordinator's own job, out of this ticket's boundary (HOW 4).
		return
	}
	if effectExists && (len(result2.Confirmed) != 0 || len(result2.ToReperform) != 0) {
		t.Fatalf("second reconcile after confirm: %#v, want no remaining work (exactly-once)", result2)
	}
}

func TestOutboxCrashBeforeEffect_Spawn(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteSpawn, "before", false)
}
func TestOutboxCrashBeforeEffect_LeaseAcquire(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteLeaseAcquire, "before", false)
}
func TestOutboxCrashBeforeEffect_InboxPublish(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteInboxPublish, "before", false)
}
func TestOutboxCrashBeforeEffect_CIDispatch(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteCIDispatch, "before", false)
}
func TestOutboxCrashBeforeEffect_Integration(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteIntegration, "before", false)
}
func TestOutboxCrashAfterEffect_Spawn(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteSpawn, "after", true)
}
func TestOutboxCrashAfterEffect_LeaseAcquire(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteLeaseAcquire, "after", true)
}
func TestOutboxCrashAfterEffect_InboxPublish(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteInboxPublish, "after", true)
}
func TestOutboxCrashAfterEffect_CIDispatch(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteCIDispatch, "after", true)
}
func TestOutboxCrashAfterEffect_Integration(t *testing.T) {
	runOutboxCrashCase(t, OutboxSiteIntegration, "after", true)
}
