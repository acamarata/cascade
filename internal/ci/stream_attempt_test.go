// Purpose: attempts and tombstones: one current attempt per lease, a late
// result for a superseded attempt changes nothing, and one (attempt, kind)
// never runs twice at once.
//
// SPORT: internal.ci.ci_stream_attempt/TESTED (P1-CI-01).
package ci

import (
	"context"
	"sync"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestOpenAttemptConflictRollsBackTombstone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	row := attemptRow{AttemptID: "original", JobID: r.ref.JobID, LeaseID: leaseKey(r.lease), CheckpointID: "checkpoint"}
	if err := r.d.openAttempt(ctx, row); err != nil {
		t.Fatal(err)
	}
	err := r.d.openAttempt(ctx, row)
	assertStreamError(t, err, cascade.KindConflict, "open attempt original")
	got, ok, err := r.d.getAttempt(ctx, row.AttemptID)
	if err != nil || !ok || got.State != attemptLive || got.CheckpointID != row.CheckpointID {
		t.Fatalf("failed insert retired existing attempt: %+v, %v, %v", got, ok, err)
	}
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_stream_attempt`); n != 1 {
		t.Fatalf("failed insert changed attempt count to %d", n)
	}
}

func TestAttemptCorruptionIsIntegrityFailure(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.d.openAttempt(ctx, attemptRow{AttemptID: "corrupt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ciDB.Exec(`UPDATE ci_stream_attempt SET dispatches = ? WHERE attempt_id = ?`, "{broken", "corrupt"); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.d.getAttempt(ctx, "corrupt")
	assertStreamError(t, err, cascade.KindIntegrity, "attempt corrupt: unreadable dispatches")
	err = r.d.appendEntry(ctx, "corrupt", dispatchEntry{Kinds: []RequirementKind{RequirementUnit}})
	assertStreamError(t, err, cascade.KindIntegrity, "unreadable dispatches")
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_stream_attempt WHERE dispatches = ?`, "{broken"); n != 1 {
		t.Fatal("append replaced the corrupt dispatch record")
	}
	err = r.d.appendEntry(ctx, "missing", dispatchEntry{})
	assertStreamError(t, err, cascade.KindUnavailable, "read dispatches")
}

func TestAttemptStorageFailuresAreUnavailable(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.ciDB.Close(); err != nil {
		t.Fatal(err)
	}
	for message, call := range map[string]func() error{
		"begin attempt":            func() error { return r.d.openAttempt(ctx, attemptRow{}) },
		"begin dispatch record":    func() error { return r.d.appendEntry(ctx, "a", dispatchEntry{}) },
		"tombstone attempts":       func() error { return r.d.tombstoneOthers(ctx, "lease", "keep") },
		"tombstone fenced attempt": func() error { return r.d.tombstone(ctx, "a") },
		"set attempt state":        func() error { return r.d.setState(ctx, "a", attemptLive, attemptTerminal) },
		"query attempts":           func() error { return r.d.requireCurrent(ctx, "a") },
	} {
		t.Run(message, func(t *testing.T) { assertStreamError(t, call(), cascade.KindUnavailable, message) })
	}
}

func TestAttemptUpdateFailureRollsBackDispatch(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.d.openAttempt(ctx, attemptRow{AttemptID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ciDB.Exec(`CREATE TRIGGER refuse_attempt_update BEFORE UPDATE ON ci_stream_attempt
		BEGIN SELECT RAISE(ABORT, 'attempt writes refused'); END`); err != nil {
		t.Fatal(err)
	}
	assertStreamError(t, r.d.openAttempt(ctx, attemptRow{AttemptID: "b"}), cascade.KindUnavailable, "tombstone before open")
	assertStreamError(t, r.d.appendEntry(ctx, "a", dispatchEntry{}), cascade.KindUnavailable, "write dispatches")
	got, ok, err := r.d.getAttempt(ctx, "a")
	if err != nil || !ok || got.State != attemptLive || len(got.Entries) != 0 {
		t.Fatalf("refused update changed row: %+v, %v, %v", got, ok, err)
	}
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_stream_attempt`); n != 1 {
		t.Fatalf("refused open created an attempt: %d", n)
	}
}

func TestOneInFlightPerLease(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.commit(map[string]string{"docs/a.md": "a"})
	first, err := r.checkpoint()
	if err != nil {
		t.Fatalf("checkpoint 1: %v", err)
	}
	r.commit(map[string]string{"docs/b.md": "b"})
	second, err := r.checkpoint()
	if err != nil {
		t.Fatalf("checkpoint 2: %v", err)
	}
	if first.AttemptID == second.AttemptID {
		t.Fatal("two checkpoints shared an attempt")
	}
	if st := attemptState(t, r, first.AttemptID); st != attemptTombstoned {
		t.Fatalf("first attempt is %q, want tombstoned", st)
	}
	lease := leaseKey(r.lease)
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_stream_attempt WHERE lease_id = ? AND state != ?`, lease, attemptTombstoned); n != 1 {
		t.Fatalf("%d current attempts for the lease, want exactly 1", n)
	}
	cur, ok, err := r.d.CurrentCheckpoint(ctx, "job-1")
	if err != nil || !ok || cur.AttemptID != second.AttemptID || cur.CheckpointID != second.CheckpointID || cur.TreeHash != second.TreeHash {
		t.Fatalf("CurrentCheckpoint = %+v ok=%v err=%v, want the second checkpoint %+v", cur, ok, err, second)
	}

	before := r.exec.count()
	if again, err := r.checkpoint(); err != nil || again.AttemptID != second.AttemptID || r.exec.count() != before {
		t.Fatalf("repeating the current checkpoint: %+v, %v, runs %d->%d; want the same attempt and no new work", again, err, before, r.exec.count())
	}
	r.ref.CheckpointCommit = gitOutput(t, r.wt, "rev-parse", "HEAD~1")
	if _, err := r.checkpoint(); !errChainHas(err, ErrCheckpointStale) {
		t.Fatalf("repeating the superseded checkpoint: err = %v, want ErrCheckpointStale", err)
	}
	if st := attemptState(t, r, second.AttemptID); st == attemptTombstoned {
		t.Fatal("refusing a stale checkpoint must not tombstone the current attempt")
	}
}

// supersedeMidRun starts checkpoint A with its sub-jobs blocked in the
// executor, supersedes it with checkpoint B, releases A and returns B's
// snapshot, A's error and A's commit.
func supersedeMidRun(t *testing.T, r *streamRig) (CandidateSnapshot, error) {
	t.Helper()
	r.commit(map[string]string{"docs/a.md": "a"})
	shaA := r.ref.CheckpointCommit
	started, release := make(chan struct{}, 8), make(chan struct{})
	r.exec.fn = func(sj SubJob) (SubJobResult, error) {
		if sj.Ref.CheckpointCommit == shaA {
			started <- struct{}{}
			<-release
		}
		return SubJobResult{RunID: -int64(r.exec.count()), RepoID: 7, Passed: true, ExecutorKind: "fake"}, nil
	}
	var wg sync.WaitGroup
	var errA error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errA = r.checkpoint()
	}()
	<-started // checkpoint A has a sub-job in flight
	r.commit(map[string]string{"docs/b.md": "b"})
	snapB, err := r.checkpoint()
	if err != nil {
		t.Fatalf("checkpoint B: %v", err)
	}
	close(release)
	wg.Wait()
	return snapB, errA
}

// TestLateResultRejected covers both ways a result can be late: a sub-job
// still running when a newer checkpoint supersedes its attempt, and a
// dispatch against an already-superseded attempt.
func TestLateResultRejected(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	snapB, errA := supersedeMidRun(t, r)
	if !errChainHas(errA, ErrLateResult) {
		t.Fatalf("superseded checkpoint: err = %v, want ErrLateResult", errA)
	}
	terminals := r.streamEvents(EventKindCheckpointTerminal)
	if len(terminals) != 2 {
		t.Fatalf("terminal events = %d, want exactly checkpoint B's 2 (positive control: B's published, A's did not)", len(terminals))
	}
	for _, p := range terminals {
		if p.CheckpointID != snapB.CheckpointID {
			t.Fatalf("terminal event for checkpoint %s, want only B's %s", p.CheckpointID, snapB.CheckpointID)
		}
	}
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_run_stream WHERE checkpoint_id != ?`, snapB.CheckpointID); n != 0 {
		t.Fatalf("%d ci_run_stream rows for the superseded checkpoint, want 0", n)
	}
	open, err := r.d.unconfirmedKeys(ctx)
	if err != nil || len(open) != 2 {
		t.Fatalf("unconfirmed rows = %d (%v), want A's 2 left in intent", len(open), err)
	}

	// A dispatch against the superseded snapshot is refused before any work.
	runs, dispatched := r.exec.count(), len(r.streamEvents(EventKindCheckpointDispatched))
	var staleID string
	if err := r.ciDB.QueryRow(`SELECT attempt_id FROM ci_stream_attempt WHERE state = ?`, attemptTombstoned).Scan(&staleID); err != nil {
		t.Fatalf("the superseded attempt must exist: %v", err)
	}
	stale := CandidateSnapshot{AttemptID: staleID}
	if _, err := r.d.Dispatch(ctx, r.ref, stale, CIRequirementPlan{Requirement: CIRequirement{Format: true}}, false); !errChainHas(err, ErrLateResult) {
		t.Fatalf("Dispatch on a tombstoned attempt: err = %v, want ErrLateResult", err)
	}
	if r.exec.count() != runs || len(r.streamEvents(EventKindCheckpointDispatched)) != dispatched {
		t.Fatal("a refused dispatch ran a sub-job or published an event")
	}

	rep, err := r.d.Resume(ctx)
	if err != nil || rep.Dropped != 2 || rep.Redispatched != 0 || r.exec.count() != runs {
		t.Fatalf("Resume = %+v, %v, runs %d->%d; want A's 2 intents dropped and nothing re-run", rep, err, runs, r.exec.count())
	}
	if open, _ := r.d.unconfirmedKeys(ctx); len(open) != 0 {
		t.Fatalf("%d rows still unconfirmed after the drop", len(open))
	}
}

func TestDispatchRefusesTheSameKindRunningTwice(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, err := r.checkpoint()
	if err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	started, release := make(chan struct{}, 4), make(chan struct{})
	r.exec.fn = func(SubJob) (SubJobResult, error) {
		started <- struct{}{}
		<-release
		return SubJobResult{RunID: -99, RepoID: 7, Passed: true, ExecutorKind: "fake"}, nil
	}
	plan := CIRequirementPlan{Requirement: CIRequirement{Integration: true}, RiskClass: "low"}
	var wg sync.WaitGroup
	wg.Add(1)
	var firstErr error
	go func() {
		defer wg.Done()
		_, firstErr = r.d.Dispatch(ctx, r.ref, snap, plan, false)
	}()
	<-started
	_, err = r.d.Dispatch(ctx, r.ref, snap, plan, false)
	close(release)
	wg.Wait()
	if !errChainHas(err, ErrAlreadyRunning) {
		t.Fatalf("second concurrent dispatch: err = %v, want ErrAlreadyRunning", err)
	}
	if firstErr != nil {
		t.Fatalf("first dispatch: %v", firstErr)
	}
}
