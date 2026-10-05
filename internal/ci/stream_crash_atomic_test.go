// Purpose: the two crash proofs added by P1-CI-01's first code review, on
// the SEC-33 kill-site recipe of stream_crash_test.go: a kill between the
// ci_job insert and the ci_step inserts must leave no step-less job
// (TestExecuteJobAndStepsAtomic), and a kill between opening an attempt
// and recording its first dispatch must not strand the attempt (a retried
// Checkpoint dispatches it exactly once).
//
// SPORT: internal.ci.Dispatcher.Checkpoint/TESTED (P1-CI-01).
package ci

import (
	"context"
	goruntime "runtime"
	"testing"
)

// TestExecuteJobAndStepsAtomic kills the child inside the transaction that
// writes ci_run, ci_job and ci_step, right after the ci_job insert. A
// non-transactional write would leave a completed job with no step rows
// that Resume then accepts as done; the transaction leaves neither, and
// Resume re-runs the sub-job to a job with its step.
func TestExecuteJobAndStepsAtomic(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the kill-site child needs POSIX process semantics for the shell stand-ins")
	}
	f := newCrashFixture(t)
	runCrashChild(t, f, "jobtx:after")

	d, fx, ciDB, jobsDB := f.dispatcher(t, "sqlite", nil)
	defer func() { _ = ciDB.Close(); _ = jobsDB.Close() }()
	if countRows(t, ciDB, `SELECT COUNT(*) FROM ci_run`) < 1 {
		t.Fatal("the kill did not land after the sub-job reserved its ci_run")
	}
	if n := countRows(t, ciDB, `SELECT COUNT(*) FROM ci_job j WHERE NOT EXISTS (SELECT 1 FROM ci_step s WHERE s.job_id = j.job_id)`); n != 0 {
		t.Fatalf("the kill left %d ci_job rows with no ci_step row", n)
	}
	if _, err := d.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	assertResumedExactlyOnce(t, ciDB, jobsDB)
	if len(fx.commands()) == 0 {
		t.Fatal("Resume ran no command although the kill left the sub-jobs unfinished")
	}
}

// TestCheckpointRetryAfterAttemptOpen kills the child after openAttempt
// committed and before appendEntry recorded the first dispatch. A retried
// Checkpoint must treat the entry-less attempt as not done and dispatch
// both kinds exactly once.
func TestCheckpointRetryAfterAttemptOpen(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the kill-site child needs POSIX process semantics for the shell stand-ins")
	}
	f := newCrashFixture(t)
	runCrashChild(t, f, "attempt:after")

	d, fx, ciDB, jobsDB := f.dispatcher(t, "sqlite", nil)
	defer func() { _ = ciDB.Close(); _ = jobsDB.Close() }()
	if countRows(t, ciDB, `SELECT COUNT(*) FROM ci_stream_attempt WHERE dispatches = '[]'`) != 1 ||
		countRows(t, jobsDB, `SELECT COUNT(*) FROM jobs_outbox`) != 0 {
		t.Fatal("the kill did not leave a live attempt with no dispatch entry and no outbox row")
	}
	if _, err := d.Checkpoint(context.Background(), f.ref(), RequirementModel{}); err != nil {
		t.Fatalf("retried Checkpoint: %v", err)
	}
	assertResumedExactlyOnce(t, ciDB, jobsDB)
	ran := len(fx.commands())
	if ran != 2 {
		t.Fatalf("the retry ran %d commands, want exactly 2 (one per kind)", ran)
	}
	again, err := d.Resume(context.Background())
	if err != nil || again != (ResumeReport{}) || len(fx.commands()) != ran {
		t.Fatalf("Resume after the retry = %+v, %v, commands %d->%d; nothing may run twice", again, err, ran, len(fx.commands()))
	}
}

type killBeforeExec struct{ SubJobExecutor }

func (k killBeforeExec) Run(ctx context.Context, sj SubJob) (SubJobResult, error) {
	killSelf()
	return k.SubJobExecutor.Run(ctx, sj)
}
