//go:build !windows

// Purpose: prove learn jobs through the daemon's real scheduler and SQLite.
// Inputs: isolated homes, persisted jobs and an injected clock.
// Outputs: scheduled jobs, outcome rows and retention events.
// Constraints: no real HOME, network, vault or background test work.
// SPORT: cmd/cascade/daemon learn job verification.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
)

func newLearnTestScheduler(t *testing.T, roots ...string) (*scheduler.Scheduler, *sql.DB, *events.Bus, fakeDaemonPaths, *testkit.FrozenClock) {
	t.Helper()
	paths := fakeDaemonPaths{root: t.TempDir()}
	if len(roots) != 0 {
		paths.root = roots[0]
	}
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, paths.root)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store, db, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeStore)
	bus := events.New(store, clock)
	cfg := &runtime.Config{}
	pol, err := wirePolicy(ctx, store, clock, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sched, _, cleanup, err := startScheduler(ctx, testManifest(), store, db, paths, cfg, clock, bus,
		slog.New(slog.NewTextHandler(testWriter{t}, nil)), pol.Router)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); cleanup(context.Background()) })
	return sched, db, bus, paths, clock
}

func tickLearnJob(t *testing.T, sched *scheduler.Scheduler, clock *testkit.FrozenClock, owner string, wantErrors ...string) {
	t.Helper()
	clock.Advance(90 * time.Second)
	report, err := sched.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var gotErrors []string
	for _, err := range report.Errors {
		gotErrors = append(gotErrors, err.Error())
	}
	if !slices.Equal(gotErrors, wantErrors) || !slices.Contains(report.Fired, owner) {
		t.Errorf("job %s: fired=%v errors=%v, want errors=%v", owner, report.Fired, report.Errors, wantErrors)
	}
}

func seedLearnJob(t *testing.T, db *sql.DB, state jobs.JobState) string {
	t.Helper()
	id := "job-" + string(state)
	err := jobs.NewStore(db).PutJob(context.Background(), jobs.Job{
		ID: id, State: state, CreatedAt: 1, UpdatedAt: 2,
		Capabilities: []string{"code"}, MutableScope: "repo:/tmp/x", RiskClass: "normal",
		MinTaskClass: "code", NodeRequirements: "{}", TimeoutSeconds: 60, CostCeiling: 1,
		Priority: 1, ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func learnCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestLearnJobsRegisteredBeforeActivate(t *testing.T) {
	t.Run("schema failure refuses startup", testLearnSchemaFailure)
	sched, db, _, _, clock := newLearnTestScheduler(t)
	list, err := sched.ListScheduledJobs()
	if err != nil {
		t.Fatal(err)
	}
	for owner, spec := range map[string]string{learnOutcomeReconcileOwner: "@every 1m0s", learnRetentionOwner: "@every 24h0m0s"} {
		found := 0
		for _, job := range list {
			if job.Owner == owner && job.ID == owner && job.Spec == spec {
				found++
			}
		}
		if found != 1 {
			t.Errorf("%s: got %d scheduled definitions, want 1", owner, found)
		}
	}
	if len(sched.OrphanedJobs()) != 0 {
		t.Fatalf("orphaned: %v", sched.OrphanedJobs())
	}
	if n := learnCount(t, db, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('jobs_telemetry_outcomes','jobs_telemetry_finding','jobs_capability_score_observations','jobs_scheduler_decisions')`); n != 4 {
		t.Fatalf("learn migration tables=%d, want 4", n)
	}
	tickLearnJob(t, sched, clock, learnOutcomeReconcileOwner)
}

func testLearnSchemaFailure(t *testing.T) {
	t.Helper()
	paths := fakeDaemonPaths{root: t.TempDir()}
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, paths.root)
	}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, db, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS applied_migrations (id INTEGER PRIMARY KEY, schema_version INTEGER, checksum TEXT, applied_at INTEGER, set_id TEXT); INSERT INTO applied_migrations (schema_version, checksum, applied_at, set_id) VALUES (999, 'future-learn-schema', 1, 'learn')`); err != nil {
		t.Fatal(err)
	}
	pol, err := wirePolicy(ctx, store, clock, &runtime.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sched, _, cleanup, err := startScheduler(ctx, testManifest(), store, db, paths, &runtime.Config{}, clock,
		events.New(store, clock), slog.New(slog.NewTextHandler(testWriter{t}, nil)), pol.Router)
	if cleanup != nil {
		cancel()
		cleanup(context.Background())
	}
	if err == nil || !strings.Contains(err.Error(), "on-disk schema_version 999 exceeds binary reader_ceiling") || sched != nil {
		t.Fatalf("schema error allowed startup: scheduler=%v err=%v", sched, err)
	}
	probe := scheduler.New(store, schedulerNamespace, clock, nil, "schema-probe", time.Minute)
	list, err := probe.ListScheduledJobs()
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range list {
		if job.Owner == learnOutcomeReconcileOwner || job.Owner == learnRetentionOwner {
			t.Fatalf("schema failure left scheduled learn job: %v", job)
		}
	}
}

func TestOutcomeRecordedOnAccepted(t *testing.T) {
	sched, db, _, _, clock := newLearnTestScheduler(t)
	id := seedLearnJob(t, db, jobs.JobStateAccepted)
	tickLearnJob(t, sched, clock, learnOutcomeReconcileOwner)
	if n := learnCount(t, db, `SELECT count(*) FROM jobs_telemetry_outcomes WHERE job_id=? AND final_outcome='accepted'`, id); n != 1 {
		t.Fatalf("accepted outcomes=%d, want 1", n)
	}
}

func TestOutcomeReconcilerRecordsEachTerminalJobOnce(t *testing.T) {
	root := t.TempDir()
	states := []jobs.JobState{jobs.JobStateAccepted, jobs.JobStateRejected, jobs.JobStateCancelled, jobs.JobStateFailed}
	var first string
	for run := range 2 {
		t.Run([]string{"initial", "restart"}[run], func(t *testing.T) {
			sched, db, _, _, clock := newLearnTestScheduler(t, root)
			if run == 0 {
				for _, state := range states {
					seedLearnJob(t, db, state)
				}
			}
			tickLearnJob(t, sched, clock, learnOutcomeReconcileOwner)
			for _, state := range states {
				if n := learnCount(t, db, `SELECT count(*) FROM jobs_telemetry_outcomes WHERE job_id=?`, "job-"+string(state)); n != 1 {
					t.Fatalf("run %d state %s: rows=%d, want 1", run, state, n)
				}
			}
			var snapshot string
			if err := db.QueryRowContext(context.Background(), `SELECT json_group_array(json_array(id, job_id, task_class, repo_id, language, component, risk_class, lane_tier, node_id, scope_ref, context_size_tokens, retrieval_strategy, duration_ms, queue_time_ms, retry_count, ci_failure_count, rework_cycles, final_outcome, rollback_at, regression_detected, cost_tokens, quota_units, created_at)) FROM (SELECT * FROM jobs_telemetry_outcomes ORDER BY id)`).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			if run == 0 {
				first = snapshot
			} else if snapshot != first {
				t.Fatalf("restart reconciliation changed stored outcomes: %s -> %s", first, snapshot)
			}
		})
	}
}

func TestOutcomeReconcileReachesObservationWriter(t *testing.T) {
	original := newLearnOutcomeWriter
	var composed *learn.SQLiteOutcomeWriter
	newLearnOutcomeWriter = func(db *sql.DB, clock runtime.Clock) *learn.SQLiteOutcomeWriter {
		composed = original(db, clock)
		return composed
	}
	t.Cleanup(func() { newLearnOutcomeWriter = original })
	sched, db, _, _, clock := newLearnTestScheduler(t)
	if composed == nil {
		t.Fatal("registered reconciler did not construct its production writer")
	}
	obs := reflect.ValueOf(composed).Elem().FieldByName("obs")
	if !obs.IsValid() || obs.IsNil() || obs.Elem().Type() != reflect.TypeFor[*learn.SQLiteCapabilityScorer]() {
		t.Fatal("registered writer lacks the production capability observation writer")
	}
	id := seedLearnJob(t, db, jobs.JobStateAccepted)
	tickLearnJob(t, sched, clock, learnOutcomeReconcileOwner)
	if n := learnCount(t, db, `SELECT count(*) FROM jobs_telemetry_outcomes WHERE job_id=? AND repo_id='unknown' AND lane_tier='unknown'`, id); n != 1 {
		t.Fatalf("neutral outcomes=%d, want 1", n)
	}
	if n := learnCount(t, db, `SELECT count(*) FROM jobs_capability_score_observations`); n != 0 {
		t.Fatalf("neutral job fabricated %d observations", n)
	}
}

// TestOutcomeReconcileUpdatesObservations retains the verification selector for neutral metadata.
func TestOutcomeReconcileUpdatesObservations(t *testing.T) {
	TestOutcomeReconcileReachesObservationWriter(t)
}

func TestLearnRetentionReadsConfigPerRun(t *testing.T) { testLearnRetention(t, false) }
func TestLearnRetentionPublishFailure(t *testing.T)    { testLearnRetention(t, true) }
func testLearnRetention(t *testing.T, refusePublish bool) {
	t.Helper()
	sched, db, bus, paths, clock := newLearnTestScheduler(t)
	seedLearnJob(t, db, jobs.JobStateAccepted)
	tickLearnJob(t, sched, clock, learnOutcomeReconcileOwner)
	if err := sched.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := sched.ScheduleJob(context.Background(), learnRetentionOwner, "@every 1m0s", learnRetentionOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := sched.Activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE jobs_telemetry_outcomes SET created_at=?`, clock.Now().AddDate(0, 0, -10).Unix()); err != nil {
		t.Fatal(err)
	}
	for i, days := range []string{"30", "1"} {
		if err := os.WriteFile(paths.ConfigPath(), []byte("[learn.retention]\nmax_age_days = "+days+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var wantErrors []string
		if refusePublish && i == 1 {
			if err := bus.Close(); err != nil {
				t.Fatal(err)
			}
			wantErrors = []string{"unavailable: events: Publish called after Close"}
		}
		tickLearnJob(t, sched, clock, learnRetentionOwner, wantErrors...)
		if n := learnCount(t, db, `SELECT count(*) FROM jobs_telemetry_outcomes`); n != 1-i {
			t.Fatalf("run %d: retained rows=%d, want %d", i, n, 1-i)
		}
	}
	if !refusePublish {
		assertLearnRetentionEvents(t, bus)
	}
}
func assertLearnRetentionEvents(t *testing.T, bus *events.Bus) {
	t.Helper()
	list, err := bus.Replay(context.Background(), learnEventNamespace, 0)
	if err != nil {
		t.Fatal(err)
	}
	var counts []int64
	for _, event := range list {
		if event.Kind == EventKindLearnRetentionSwept {
			var payload learnRetentionSweptPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			counts = append(counts, payload.RowsDeleted)
		}
	}
	if !slices.Equal(counts, []int64{0, 1}) {
		t.Fatalf("swept counts=%v, want [0 1]", counts)
	}
}
