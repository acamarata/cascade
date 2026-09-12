package jobs

// Purpose: the HOW 3 kill -9 mid-DAG test: a REAL child OS process
// appends a real M/S-27.T1 journal entry, is REAL SIGKILLed mid-DAG, and
// the restarted Resume replays the on-disk journal and re-enters the
// interrupted job at `leased`, after which Advance moves it on to
// `running`. Split out of scheduler_resume_test.go to keep both files
// under the 300-line cap.
//
// CONTRACT NOTE (quoted in the journal): as with the outbox crash matrix
// (migration_outbox_test.go), this file uses the real self-exec
// subprocess idiom (submit_kill9_test.go's TestResumeKillHelperProcess)
// rather than the non-existent testkit.SpawnDaemon.
//
// SPORT: jobs/scheduler/ADD (tests) (P1-E29-W6-S59-T5).

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/providers/sqlite"
)

// realJournalReader adapts the REAL internal/fleet/journal.Store to this
// package's Reader seam -- proving HOW 3's "no direct import of
// internal/fleet/journal from this package" boundary from the test side
// (production code never imports it; only this test-only adapter does).
type realJournalReader struct{ store journal.Store }

func (r realJournalReader) Replay(ctx context.Context, entityID string, _ int64) ([]JournalEntry, error) {
	entries, err := r.store.Replay(ctx, entityID, journal.Cursor{}, nil)
	if err != nil {
		return nil, err
	}
	out := make([]JournalEntry, len(entries))
	for i, e := range entries {
		out[i] = JournalEntry{EntityID: e.EntityID, Cursor: int64(e.Seq), Kind: fmt.Sprintf("%d", e.Kind)}
	}
	return out, nil
}

const killTestEnv = "CASCADE_SCHEDULER_KILLTEST_DB_PATH"

// TestSchedulerKillHelperProcess is re-executed as a SEPARATE OS PROCESS.
// Unset env is a no-op ordinary test run.
func TestSchedulerKillHelperProcess(_ *testing.T) {
	path := os.Getenv(killTestEnv)
	if path == "" {
		return
	}
	ctx := context.Background()
	driver, err := sqlite.Open(ctx, path)
	if err != nil {
		_, _ = os.Stdout.WriteString("OPEN_FAILED\n")
		return
	}
	j := journal.New(driver, runtime.NewFixedClock(time.Unix(9999, 0)), journal.DefaultNamespace)
	if _, err := j.Append(ctx, "killed-job", journal.KindIntent, "progress-1", []byte(`{}`)); err != nil {
		_, _ = os.Stdout.WriteString("SEED_FAILED\n")
		return
	}
	_, _ = os.Stdout.WriteString("READY\n")
	_, _ = bufio.NewReader(os.Stdin).ReadByte() // block until SIGKILLed; never reached; driver.Close() never runs
}

func spawnAndKillSchedulerHelper(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSchedulerKillHelperProcess$")
	cmd.Env = append(os.Environ(), killTestEnv+"="+path)
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
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	_ = cmd.Wait()
}

// TestResumeKill9MidDAG is the HOW 3 / acceptance-criteria kill -9
// mid-DAG test: a real child process appends a real journal entry for
// "killed-job" and is REAL SIGKILLed before it can close anything;
// Resume then replays that SAME on-disk journal and re-enters the job
// at `leased`, and a following Advance moves it on to `running`.
// killDAGTestStore migrates a fresh jobs+outbox db and seeds one stale
// `running` job -- TestResumeKill9MidDAG's funlen-cap split.
func killDAGTestStore(t *testing.T) *Store {
	t.Helper()
	jobsDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("open jobs db: %v", err)
	}
	jobsDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = jobsDB.Close() })
	if err := ApplyJobsSchema(context.Background(), jobsDB, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyJobsSchema: %v", err)
	}
	if err := ApplyOutboxSchema(context.Background(), jobsDB, migrate.SQLiteEmitter{}, newTestClock(), "", ""); err != nil {
		t.Fatalf("ApplyOutboxSchema: %v", err)
	}
	store := NewStore(jobsDB)
	seedRunningJobWithLease(t, store, "killed-job", 0, LeaseHeld) // issued at 0: any positive "now" is stale
	return store
}

func TestResumeKill9MidDAG(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "journal.db")
	spawnAndKillSchedulerHelper(t, dbPath)
	store := killDAGTestStore(t)

	driver, err := sqlite.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("reopen journal db after kill: %v", err)
	}
	t.Cleanup(func() { _ = driver.Close() })
	realJournal := journal.New(driver, runtime.NewFixedClock(time.Unix(20_000, 0)), journal.DefaultNamespace)

	sched := NewScheduler(runtime.NewFixedClock(time.Unix(20_000, 0)))
	events, err := sched.Resume(controllerCtx(), store, realJournalReader{store: realJournal}, 0, nil, nil)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want exactly one re-entry for killed-job", events)
	}
	jt, ok := events[0].(JobTransitioned)
	if !ok || jt.JobID != "killed-job" || jt.To != JobStateLeased {
		t.Fatalf("events[0] = %#v, want JobTransitioned{killed-job, ->leased}", events[0])
	}

	// CONTRACT NOTE (journal): state.go's publicEdges (S-59.T1, out of
	// this ticket's scope) has no running->leased edge, so PutTransition
	// would refuse the very re-entry HOW 3/R-16.68c requires; Advance
	// below is driven from the jobStates ARGUMENT, per its pure contract.
	dag := ExecutionDag{Nodes: []DagNode{node("killed-job", 1, "a/**")}}
	leases, err := store.allLeasesForHolder(context.Background(), "killed-job")
	if err != nil {
		t.Fatalf("allLeasesForHolder: %v", err)
	}
	var calls int
	delta := sched.Advance(controllerCtx(), dag, jt, map[string]JobState{"killed-job": JobStateLeased}, leases, allowAllGovernor(&calls))
	found := false
	for _, tr := range delta.JobsToAdvance {
		if tr.JobID == "killed-job" && tr.From == JobStateLeased && tr.To == JobStateRunning {
			found = true
		}
	}
	if !found {
		t.Fatalf("Advance after re-entry: JobsToAdvance = %#v, want killed-job leased->running", delta.JobsToAdvance)
	}
}
