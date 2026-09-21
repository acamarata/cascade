// Purpose: waitmerge_checks.go's decision table, driven directly with the
// exact inputs the adversarial review used. Each case names the mutation
// that turns it red.
//
// SPORT: internal.ci.evaluateChecks/TESTED (P1-E25-W5-S51-T3).
package ci

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func job(name string, status RunStatus, conclusion RunConclusion) Job {
	return Job{RunID: 501, Name: name, Status: status, Conclusion: conclusion}
}

func doneJob(name string, conclusion RunConclusion) Job {
	return job(name, RunStatusCompleted, conclusion)
}

func autoOpts() WaitOptions {
	return WaitOptions{Owner: "acamarata", Repo: "cascade", Ref: "main"}
}

// TestEvaluateChecks_PartialSnapshotOfAnInProgressRunIsNotGreen is the
// REVIEW'S OWN INPUT (finding 2): run 501 is still in_progress and the only
// job that has appeared so far succeeded, while a `needs: build` job or an
// unexpanded matrix leg does not exist yet. Deriving the required set from
// the jobs present in THIS poll and calling it green would merge a PR whose
// test job never ran.
//
// MUTATION TARGET (b): deleting the `run.Status != RunStatusCompleted`
// guard in evaluateChecks turns this green.
func TestEvaluateChecks_PartialSnapshotOfAnInProgressRunIsNotGreen(t *testing.T) {
	run := Run{RunID: 501, Status: RunStatusInProgress, HeadSHA: "deadbeef"}
	resolved, checks, err := evaluateChecks(autoOpts(), run, []Job{doneJob("build", ConclusionSuccess)})
	if err != nil {
		t.Fatalf("err = %v, want nil (still waiting, not an abort)", err)
	}
	if resolved {
		t.Fatal("resolved = true for an in_progress run: a partial job snapshot must never resolve green")
	}
	if len(checks) != 1 || checks[0].Name != "build" {
		t.Fatalf("checks = %+v, want the one job observed so far", checks)
	}
}

// TestEvaluateChecks_ExplicitRequiredSetStillNeedsTheRunItself is the
// CONFIRMING REVIEW'S input (finding 2): RequiredChecks=["build"], run 501
// still in_progress, and the named job already completed successfully.
// Before the fix the run gate only applied when RequiredChecks was empty,
// so this resolved green -- contradicting D2 and
// docs/cli-reference/github-ci.md, both of which state the rule
// unconditionally.
//
// MUTATION TARGET: putting the run gate back behind
// `if len(opts.RequiredChecks) == 0` turns this green.
func TestEvaluateChecks_ExplicitRequiredSetStillNeedsTheRunItself(t *testing.T) {
	opts := autoOpts()
	opts.RequiredChecks = []string{"build"}
	run := Run{RunID: 501, Status: RunStatusInProgress, HeadSHA: "deadbeef"}
	resolved, _, err := evaluateChecks(opts, run, []Job{doneJob("build", ConclusionSuccess)})
	if err != nil {
		t.Fatalf("err = %v, want nil (still waiting, not an abort)", err)
	}
	if resolved {
		t.Fatal("resolved = true: an explicit required set does not exempt the in_progress run gate")
	}
	// The same explicit set on a COMPLETED, successful run does resolve,
	// so the assertion above is not passing because explicit mode never
	// resolves.
	done := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess, HeadSHA: "deadbeef"}
	if resolved, _, err := evaluateChecks(opts, done, []Job{doneJob("build", ConclusionSuccess)}); !resolved || err != nil {
		t.Fatalf("completed run: resolved=%v err=%v, want true/nil", resolved, err)
	}
}

// TestEvaluateChecks_ExplicitRequiredSetAbortsOnAFailedRun proves the
// second half of the unconditional gate in explicit mode: the named check
// is green but the RUN concluded failure, so the wait aborts fail-closed.
func TestEvaluateChecks_ExplicitRequiredSetAbortsOnAFailedRun(t *testing.T) {
	opts := autoOpts()
	opts.RequiredChecks = []string{"build"}
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionFailure}
	_, _, err := evaluateChecks(opts, run, []Job{doneJob("build", ConclusionSuccess)})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict for a run that concluded failure", err)
	}
}

// TestEvaluateChecks_AutoModeRequiresRunSuccess proves the second half of
// the auto-mode rule: a COMPLETED run whose own conclusion is not success
// aborts, even if every job the API listed looks fine.
func TestEvaluateChecks_AutoModeRequiresRunSuccess(t *testing.T) {
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionFailure}
	_, _, err := evaluateChecks(autoOpts(), run, []Job{doneJob("build", ConclusionSuccess)})
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict for a run that concluded failure", err)
	}
}

// TestEvaluateChecks_AutoModeAllGreen proves the positive auto-mode path.
func TestEvaluateChecks_AutoModeAllGreen(t *testing.T) {
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	jobs := []Job{doneJob("build", ConclusionSuccess), doneJob("docs", ConclusionSkipped)}
	resolved, checks, err := evaluateChecks(autoOpts(), run, jobs)
	if err != nil || !resolved {
		t.Fatalf("resolved=%v err=%v, want true/nil", resolved, err)
	}
	if len(checks) != 1 || checks[0].Name != "build" {
		t.Fatalf("checks = %+v, want only the non-skipped job in the auto required set", checks)
	}
}

// TestEvaluateChecks_RequiredSkippedIsNotGreen is the SURVIVING MUTANT the
// review found: nothing pinned that an explicitly required `skipped` check
// is not a pass.
//
// MUTATION TARGET (a): folding ConclusionSkipped into checkIsGreen's
// ConclusionSuccess case turns this green.
func TestEvaluateChecks_RequiredSkippedIsNotGreen(t *testing.T) {
	opts := autoOpts()
	opts.RequiredChecks = []string{"build"}
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	resolved, _, err := evaluateChecks(opts, run, []Job{doneJob("build", ConclusionSkipped)})
	if resolved {
		t.Fatal("resolved = true for a required check that was SKIPPED")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict: a skipped check never becomes a success", err)
	}
}

// TestEvaluateChecks_AllowSkippedIsTheOnlyWaySkippedCounts proves the one
// exception, and that it is opt-in per check name.
func TestEvaluateChecks_AllowSkippedIsTheOnlyWaySkippedCounts(t *testing.T) {
	opts := autoOpts()
	opts.RequiredChecks = []string{"build", "docs"}
	opts.AllowSkipped = []string{"docs"}
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	jobs := []Job{doneJob("build", ConclusionSuccess), doneJob("docs", ConclusionSkipped)}
	resolved, _, err := evaluateChecks(opts, run, jobs)
	if err != nil || !resolved {
		t.Fatalf("resolved=%v err=%v, want true/nil with docs allowed to be skipped", resolved, err)
	}
}

// TestEvaluateChecks_ExplicitRequiredMissingIsNotGreen proves "missing is
// not green" in the explicit mode too: the named check has not reported.
func TestEvaluateChecks_ExplicitRequiredMissingIsNotGreen(t *testing.T) {
	opts := autoOpts()
	opts.RequiredChecks = []string{"build", "lint"}
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	resolved, checks, err := evaluateChecks(opts, run, []Job{doneJob("build", ConclusionSuccess)})
	if err != nil || resolved {
		t.Fatalf("resolved=%v err=%v, want false/nil for an unreported required check", resolved, err)
	}
	if len(checks) != 2 || checks[1].Status != RunStatusQueued {
		t.Fatalf("checks = %+v, want a queued placeholder for lint", checks)
	}
}

// TestEvaluateChecks_NonGreenConclusionsAbort pins every conclusion the
// rule calls not-green, one input each.
func TestEvaluateChecks_NonGreenConclusionsAbort(t *testing.T) {
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	for _, conclusion := range []RunConclusion{ConclusionFailure, ConclusionCancelled, ConclusionTimedOut, ConclusionUnknown} {
		opts := autoOpts()
		opts.RequiredChecks = []string{"build"}
		_, _, err := evaluateChecks(opts, run, []Job{doneJob("build", conclusion)})
		if !cascade.HasKind(err, cascade.KindConflict) {
			t.Fatalf("conclusion %s: err = %v, want KindConflict", conclusion, err)
		}
	}
}

// TestEvaluateChecks_SuccessOnAnUnfinishedJobKeepsWaiting proves the
// contradictory-state case resolves to the weaker reading.
func TestEvaluateChecks_SuccessOnAnUnfinishedJobKeepsWaiting(t *testing.T) {
	opts := autoOpts()
	opts.RequiredChecks = []string{"build"}
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	resolved, _, err := evaluateChecks(opts, run, []Job{job("build", RunStatusInProgress, ConclusionSuccess)})
	if err != nil || resolved {
		t.Fatalf("resolved=%v err=%v, want false/nil", resolved, err)
	}
}

// TestEvaluateChecks_PendingAndEmptyAreNeverGreen covers the
// no-job-reported and still-running cases.
func TestEvaluateChecks_PendingAndEmptyAreNeverGreen(t *testing.T) {
	run := Run{RunID: 501, Status: RunStatusCompleted, Conclusion: ConclusionSuccess}
	if resolved, _, err := evaluateChecks(autoOpts(), run, nil); resolved || err != nil {
		t.Fatalf("empty job list: resolved=%v err=%v, want false/nil", resolved, err)
	}
	opts := autoOpts()
	opts.RequiredChecks = []string{"build"}
	if resolved, _, err := evaluateChecks(opts, run, []Job{job("build", RunStatusInProgress, ConclusionNone)}); resolved || err != nil {
		t.Fatalf("pending check: resolved=%v err=%v, want false/nil", resolved, err)
	}
}

// TestCheckIsGreen_UnlistedConclusionFailsClosed proves the default arm:
// a value outside normalize.go's enum is refused, never read as pending.
func TestCheckIsGreen_UnlistedConclusionFailsClosed(t *testing.T) {
	_, err := checkIsGreen("build", doneJob("build", RunConclusion("invented")), nil)
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict for an unlisted conclusion", err)
	}
}

func TestIsTransientPollError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{cascade.New(cascade.KindUnavailable, "502 from api.github.com"), true},
		{cascade.New(cascade.KindPolicyDenied, "routes to the local gate"), false},
		{cascade.New(cascade.KindPermissionDenied, "ci-poll egress refused"), false},
		{cascade.New(cascade.KindIntegrity, "unparseable page"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isTransientPollError(c.err); got != c.want {
			t.Fatalf("isTransientPollError(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestNextPollBackoff_DoublesAndCaps(t *testing.T) {
	first := nextPollBackoff(0, 10*time.Second)
	if first != 10*time.Second {
		t.Fatalf("first = %s, want the poll interval", first)
	}
	if second := nextPollBackoff(first, 10*time.Second); second != 20*time.Second {
		t.Fatalf("second = %s, want it doubled", second)
	}
	if capped := nextPollBackoff(maxPollBackoff, 10*time.Second); capped != maxPollBackoff {
		t.Fatalf("capped = %s, want %s", capped, maxPollBackoff)
	}
	if zeroBase := nextPollBackoff(0, 0); zeroBase != DefaultWaitPollInterval {
		t.Fatalf("zeroBase = %s, want the default interval", zeroBase)
	}
}
