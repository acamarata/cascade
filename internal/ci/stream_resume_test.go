// Purpose: Dispatcher.Resume's decisions, driven in-process over states a
// crash can leave (stream_crash_inject_test.go drives the real kills): an
// interrupted sub-job is re-dispatched exactly once, an effect-state row is
// only confirmed, a confirmed (terminal) sub-job is never re-dispatched, an
// unrecorded intent is ensured then run, and a row naming no recorded
// dispatch is left alone.
//
// SPORT: internal.ci.Dispatcher.Resume/TESTED (P1-CI-01).
package ci

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestResumeRefusesUnreadableAttempts(t *testing.T) {
	r := newRig(t)
	if err := r.ciDB.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rep, err := r.d.Resume(ctx)
	assertStreamError(t, err, cascade.KindUnavailable, "query attempts")
	if rep != (ResumeReport{}) || r.exec.count() != 0 {
		t.Fatalf("failed query dispatched work: %+v, %d", rep, r.exec.count())
	}
	_, ok, err := r.d.matchRow(ctx, jobs.OutboxRow{JobID: r.ref.JobID})
	assertStreamError(t, err, cascade.KindUnavailable, "query attempts")
	if ok {
		t.Fatal("failed query matched an attempt")
	}
	_, ok, err = r.d.CurrentCheckpoint(ctx, r.ref.JobID)
	assertStreamError(t, err, cascade.KindUnavailable, "query attempts")
	if ok {
		t.Fatal("failed query returned a current checkpoint")
	}
	done, err := r.d.openOrRepeat(ctx, r.ref, CandidateSnapshot{AttemptID: "missing"}, leaseKey(r.lease))
	assertStreamError(t, err, cascade.KindUnavailable, "query attempts")
	if done {
		t.Fatal("failed query marked checkpoint done")
	}
}

func TestResumeKeepsRecordedDispatchWhenIntentFails(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if err := r.d.openAttempt(ctx, attemptRow{AttemptID: "pending", JobID: r.ref.JobID}); err != nil {
		t.Fatal(err)
	}
	e := dispatchEntry{Ref: r.ref, Snapshot: CandidateSnapshot{AttemptID: "pending"}, Kinds: []RequirementKind{RequirementUnit}}
	if err := r.d.appendEntry(ctx, "pending", e); err != nil {
		t.Fatal(err)
	}
	if _, err := r.jobsDB.Exec(`CREATE TRIGGER refuse_intent BEFORE INSERT ON jobs_outbox
		BEGIN SELECT RAISE(ABORT, 'outbox writes refused'); END`); err != nil {
		t.Fatal(err)
	}
	rep, err := r.d.Resume(ctx)
	assertStreamError(t, err, cascade.KindUnavailable, "outbox writes refused")
	if rep != (ResumeReport{}) || r.exec.count() != 0 {
		t.Fatalf("failed intent dispatched work: %+v, %d", rep, r.exec.count())
	}
	a, ok, err := r.d.getAttempt(ctx, "pending")
	if err != nil || !ok || a.State != attemptLive || len(a.Entries) != 1 || !sameKinds(a.Entries[0].Kinds, e.Kinds) {
		t.Fatalf("failed intent lost replay state: %+v, %v, %v", a, ok, err)
	}
	if n := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox`); n != 0 {
		t.Fatalf("failed intent persisted %d rows", n)
	}
}

// failFirst makes the rig's executor fail its first n runs with an error.
func failFirst(r *streamRig, n int32) {
	var left atomic.Int32
	left.Store(n)
	r.exec.fn = func(SubJob) (SubJobResult, error) {
		if left.Add(-1) >= 0 {
			return SubJobResult{}, errors.New("executor down")
		}
		return SubJobResult{RunID: -int64(1000 + r.exec.count()), RepoID: 7, Passed: true, ExecutorKind: "fake"}, nil
	}
}

func TestResumeRedispatchesAnInterruptedSubJobOnce(t *testing.T) {
	r := newRig(t)
	failFirst(r, 2)
	r.commit(map[string]string{"docs/a.md": "a"})
	if _, err := r.checkpoint(); err == nil {
		t.Fatal("the failing executor must surface an error")
	}
	if open := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox WHERE state = 'intent'`); open != 2 {
		t.Fatalf("intent rows after the failure = %d, want 2", open)
	}
	before := r.exec.count()
	rep, err := r.d.Resume(context.Background())
	if err != nil || rep.Redispatched != 2 || r.exec.count()-before != 2 {
		t.Fatalf("Resume = %+v, %v, new runs %d; want both kinds re-dispatched once", rep, err, r.exec.count()-before)
	}
	if open := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox WHERE state != 'confirmed'`); open != 0 {
		t.Fatalf("%d rows unconfirmed after Resume", open)
	}
	again, err := r.d.Resume(context.Background())
	if err != nil || again != (ResumeReport{}) || r.exec.count()-before != 2 {
		t.Fatalf("second Resume = %+v, %v; a terminal sub-job must never run again", again, err)
	}
}

func TestResumeConfirmsAnEffectRowWithoutRedispatch(t *testing.T) {
	r := newRig(t)
	failFirst(r, 2)
	r.commit(map[string]string{"docs/a.md": "a"})
	snap, _ := r.checkpoint()
	att, _, err := r.d.getAttempt(context.Background(), snap.AttemptID)
	if err != nil || len(att.Entries) != 1 {
		t.Fatalf("attempt = %+v, %v", att, err)
	}
	e := att.Entries[0]
	if err := r.d.markEffect(context.Background(), outboxKeyFor(e, e.Kinds[0])); err != nil {
		t.Fatalf("markEffect: %v", err)
	}
	before := r.exec.count()
	rep, err := r.d.Resume(context.Background())
	if err != nil || rep.Confirmed != 1 || rep.Redispatched != 1 || r.exec.count()-before != 1 {
		t.Fatalf("Resume = %+v, %v, new runs %d; want 1 confirmed without a run and 1 re-dispatched", rep, err, r.exec.count()-before)
	}
}

func TestResumeEnsuresARecordedDispatchWithoutIntents(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.commit(map[string]string{"docs/a.md": "a"})
	sha := r.ref.CheckpointCommit
	snap := CandidateSnapshot{TreeHash: treeOf(t, r.repo, sha), AttemptID: "hand-made"}
	snap.CheckpointID = CheckpointIDFor("job-1", sha, snap.TreeHash)
	if err := r.d.openAttempt(ctx, attemptRow{AttemptID: snap.AttemptID, JobID: "job-1", LeaseID: leaseKey(r.lease), CheckpointID: snap.CheckpointID, TreeHash: snap.TreeHash}); err != nil {
		t.Fatalf("openAttempt: %v", err)
	}
	e := dispatchEntry{Ref: r.ref, Snapshot: snap, Plan: CIRequirementPlan{Requirement: CIRequirement{Format: true, Unit: true}},
		Kinds: []RequirementKind{RequirementFormat, RequirementUnit}, Risk: "low"}
	if err := r.d.appendEntry(ctx, snap.AttemptID, e); err != nil {
		t.Fatalf("appendEntry: %v", err)
	}
	if rows := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox`); rows != 0 {
		t.Fatalf("outbox rows = %d, want none (the crash window before the intents)", rows)
	}
	rep, err := r.d.Resume(ctx)
	if err != nil || rep.Redispatched != 2 || r.exec.count() != 2 {
		t.Fatalf("Resume = %+v, %v, runs %d; want both recorded kinds run once", rep, err, r.exec.count())
	}
	if att, _, _ := r.d.getAttempt(ctx, snap.AttemptID); att.State != attemptTerminal {
		t.Fatalf("attempt state = %q, want terminal", att.State)
	}
}

func TestResumeLeavesAnUnmatchedRowAlone(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	err := r.d.outboxTx(ctx, func(tx *sql.Tx) error {
		_, err := jobs.RecordIntent(ctx, tx, 1, jobs.OutboxIntent{JobID: "job-1", Site: jobs.OutboxSiteCIDispatch, AttemptGeneration: 1, PayloadHash: "orphan"})
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	rep, err := r.d.Resume(ctx)
	if err != nil || rep.Unmatched != 1 || r.exec.count() != 0 {
		t.Fatalf("Resume = %+v, %v, runs %d; want the orphan row reported and untouched", rep, err, r.exec.count())
	}
	if open := r.count(r.jobsDB, `SELECT COUNT(*) FROM jobs_outbox WHERE state = 'intent'`); open != 1 {
		t.Fatalf("the unmatched row changed state (intent rows %d)", open)
	}
}
