// Purpose: wait-on-green's all-green DECISION (P1-E25-W5-S51-T3, D2):
// given one poll's run + jobs, decide whether the wait is resolved green,
// must abort fail-closed, or must keep waiting. Split out of
// waitmerge_wait.go under Art.10.3's 300-line cap, and kept separate
// because this is the file a reviewer reads to check the fail-closed rule.
//
// Inputs: the WaitOptions the caller asked for, the matched Run, and that
// run's Jobs as the last poll saw them.
//
// Outputs: resolved (green), the per-check view, or a typed KindConflict
// abort.
//
// Constraints (the rule, stated once): Passed is true ONLY when
//
//   - the run itself has Status=completed and Conclusion=success, in EVERY
//     mode -- an explicitly named required set does not exempt the run
//     gate (D2 states it unconditionally, and so does
//     docs/cli-reference/github-ci.md), and
//   - every required check is PRESENT and concluded success.
//
// A required check that is missing is NOT green -- it is still waiting; a
// run still in_progress is NOT green however good the jobs that already
// reported look (the CR's input: run in_progress, jobs=[build success],
// while a `needs: build` job or an unexpanded matrix leg does not exist
// yet); and skipped/neutral/action_required/stale/cancelled/timed_out/
// failure are NEVER green. `skipped` reads as green for one case only: a
// check the caller explicitly listed in WaitOptions.AllowSkipped.
//
// SPORT: internal.ci.evaluateChecks/ADDED (P1-E25-W5-S51-T3).

package ci

import (
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// maxPollBackoff caps the retry delay a run of transient poll failures can
// grow to, so a long outage still re-checks periodically until the wait's
// own timeout decides (D7).
const maxPollBackoff = 2 * time.Minute

// findRunForRef returns the highest-RunID run matching ref by head SHA or
// head branch, so the most recent run for a ref wins when more than one
// exists.
func findRunForRef(runs []Run, ref string) (Run, bool) {
	var best Run
	found := false
	for _, r := range runs {
		if r.HeadSHA != ref && r.HeadBranch != ref {
			continue
		}
		if !found || r.RunID > best.RunID {
			best, found = r, true
		}
	}
	return best, found
}

// requiredCheckNames returns opts.RequiredChecks verbatim when the caller
// named an explicit set, else every non-skipped job name on jobs (the
// full_desc default: "all non-skipped checks for the watched workflow
// set"). In auto mode the caller has already established that the run
// COMPLETED, so the job list is final -- which is the only condition
// under which deriving the required set from the jobs present is sound.
func requiredCheckNames(opts WaitOptions, jobs []Job) []string {
	if len(opts.RequiredChecks) > 0 {
		return opts.RequiredChecks
	}
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		if j.Conclusion == ConclusionSkipped {
			continue
		}
		names = append(names, j.Name)
	}
	return names
}

// observedChecks renders every job as a CheckStatus, for the
// still-waiting returns that have no required set to report against yet.
func observedChecks(jobs []Job) []CheckStatus {
	out := make([]CheckStatus, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, CheckStatus{Name: j.Name, Status: j.Status, Conclusion: j.Conclusion})
	}
	return out
}

// evaluateChecks implements this file's header rule.
func evaluateChecks(opts WaitOptions, run Run, jobs []Job) (resolved bool, checks []CheckStatus, err error) {
	// THE RUN GATE IS UNCONDITIONAL. In auto mode the required set is only
	// knowable from the jobs present, so it is only trustworthy once the
	// run is final. In EXPLICIT mode the named checks may all be green
	// while the run is still in_progress -- a `needs:` dependent or an
	// unexpanded matrix leg that has not been created yet can still fail
	// the run -- so an unfinished run is not green there either. Applying
	// the gate before the mode split is what keeps the code, D2 and
	// docs/cli-reference/github-ci.md saying the same thing.
	if run.Status != RunStatusCompleted {
		return false, observedChecks(jobs), nil
	}
	if run.Conclusion != ConclusionSuccess {
		return false, observedChecks(jobs), cascade.Newf(cascade.KindConflict,
			"ci: wait-on-green: run %d concluded %s", run.RunID, run.Conclusion)
	}
	required := requiredCheckNames(opts, jobs)
	if len(required) == 0 {
		// A vacuous pass here would return green before any check has
		// even started, so an empty required set is never resolved.
		return false, observedChecks(jobs), nil
	}
	return evaluateRequired(required, allowSkippedSet(opts), jobs)
}

// allowSkippedSet indexes WaitOptions.AllowSkipped.
func allowSkippedSet(opts WaitOptions) map[string]bool {
	if len(opts.AllowSkipped) == 0 {
		return nil
	}
	set := make(map[string]bool, len(opts.AllowSkipped))
	for _, name := range opts.AllowSkipped {
		set[name] = true
	}
	return set
}

// evaluateRequired walks the required set against the jobs actually seen.
func evaluateRequired(required []string, allowSkipped map[string]bool, jobs []Job) (bool, []CheckStatus, error) {
	byName := make(map[string]Job, len(jobs))
	for _, j := range jobs {
		byName[j.Name] = j
	}
	checks := make([]CheckStatus, 0, len(required))
	allDone := true
	for _, name := range required {
		j, ok := byName[name]
		if !ok {
			// MISSING IS NOT GREEN: the check has not reported yet.
			allDone = false
			checks = append(checks, CheckStatus{Name: name, Status: RunStatusQueued})
			continue
		}
		checks = append(checks, CheckStatus{Name: j.Name, Status: j.Status, Conclusion: j.Conclusion})
		done, err := checkIsGreen(name, j, allowSkipped)
		if err != nil {
			return false, checks, err
		}
		if !done {
			allDone = false
		}
	}
	return allDone, checks, nil
}

// checkIsGreen decides one required check. done=true means "green"; an
// error means "abort, fail-closed"; done=false means "keep waiting".
func checkIsGreen(name string, j Job, allowSkipped map[string]bool) (bool, error) {
	switch j.Conclusion {
	case ConclusionSuccess:
		// A conclusion is only written once a job is over; a success on a
		// job the API still calls unfinished is contradictory, so it
		// keeps waiting rather than resolving on the weaker of the two.
		return j.Status == RunStatusCompleted, nil
	case ConclusionSkipped:
		if allowSkipped[name] {
			return true, nil
		}
		return false, cascade.Newf(cascade.KindConflict,
			"ci: wait-on-green: required check %q concluded skipped and is not in AllowSkipped; "+
				"a skipped check never becomes a success", name)
	case ConclusionFailure, ConclusionCancelled, ConclusionTimedOut:
		return false, cascade.Newf(cascade.KindConflict,
			"ci: wait-on-green: check %q concluded %s", name, j.Conclusion)
	case ConclusionUnknown:
		return false, cascade.Newf(cascade.KindConflict,
			"ci: wait-on-green: check %q reported an unrecognized conclusion; failing closed per 06 §5.20", name)
	case ConclusionNone:
		return false, nil
	default:
		// normalize.go's enum is closed; an unlisted value is treated as
		// the unknown case rather than silently as pending.
		return false, cascade.Newf(cascade.KindConflict,
			"ci: wait-on-green: check %q reported conclusion %q, which this build does not recognize", name, j.Conclusion)
	}
}

// isTransientPollError reports whether a poll failure is worth retrying
// (D7). ONLY KindUnavailable is: poll.go raises exactly that for a
// transport failure or a non-200 status, which is what a single
// api.github.com 502 looks like. Everything else is terminal -- a
// never-pay route refusal (KindPolicyDenied), an egress capability or
// sensitivity refusal (KindPermissionDenied), a payload that would not
// normalize (KindIntegrity), a malformed request (KindInvalidInput) -- and
// none of them resolve by asking again.
//
// The one KindUnavailable that is NOT transient, route.go's errNoPolicy
// ("no never-pay policy resolver is configured"), never reaches here:
// WaitOnGreen calls guardActionsPoll once before the loop, so a missing
// policy aborts up front instead of being retried to the deadline.
func isTransientPollError(err error) bool {
	kind, ok := cascade.KindOf(err)
	return ok && kind == cascade.KindUnavailable
}

// nextPollBackoff doubles the delay after each consecutive transient
// failure, starting from the configured poll interval and capped at
// maxPollBackoff.
func nextPollBackoff(current, base time.Duration) time.Duration {
	if base <= 0 {
		base = DefaultWaitPollInterval
	}
	if current <= 0 {
		return base
	}
	if next := current * 2; next < maxPollBackoff {
		return next
	}
	return maxPollBackoff
}
