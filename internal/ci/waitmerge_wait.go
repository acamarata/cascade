// Purpose: wait-on-green (P1-E25-W5-S51-T3, task 1-2): block until every
// required GitHub Actions check for one repo+ref reports success, or fail
// immediately on the first failure/cancellation/timeout/unrecognized
// conclusion. The all-green DECISION itself lives in
// waitmerge_checks.go; this file owns the loop, its deadline, its
// retry/abort split and the platform gate.
//
// Inputs: WaitOptions (owner/repo/ref, an optional explicit required-check
// name list, the checks a caller accepts as skipped, a timeout and a poll
// interval) and WaitDeps (the real S-51.T2 polling Client, an injected
// runtime.Clock, and an injected Sleeper).
//
// Outputs: a WaitResult with Passed=true only when every required check
// concluded success; a typed error (KindConflict on a failing/unrecognized
// conclusion, KindTimeout on deadline expiry, KindCanceled on context
// cancellation, KindUnsupported on a platform that has no daemon)
// otherwise.
//
// Constraints: no bare time.Now (forbidigo) -- every timestamp comes from
// deps.Clock; the between-poll wait goes through deps.Sleep, never a bare
// time.Sleep, so a test can prove timeout/cancellation without sleeping
// real time. The never-pay guard (route.go's guardActionsPoll) is NOT
// re-derived here: it is CALLED once up front (so a repository the policy
// keeps local is refused before the first request, and a missing policy
// aborts instead of burning the whole timeout on retries), and
// Client.PollRuns/PollJobs consult the same function again on every call,
// so this file inherits R-14.77 rather than keeping a second copy of it.
//
// SCOPE NOTE (recorded, not papered over): full_desc says wait-on-green
// "subscribes to the C/S-04.T3 event bus for ci_results domain events
// (emitted by the CI ingestion domain from S-51.T2)". That producer does
// not exist -- poll.go's and domain.go's own doc comments record that
// wiring PollRuns/PollJobs behind the event bus is a composition-root
// ticket neither S-51.T2 nor this one owns, and runner.go's
// EventKindRunCompleted publishes only for LOCAL runs. WaitOnGreen polls
// the real S-51.T2 Client directly instead, at opts.PollInterval.
//
// SPORT: internal.ci.WaitOnGreen/ADDED, internal.ci.WaitOptions/ADDED,
//
//	internal.ci.WaitDeps/ADDED, internal.ci.WaitResult/ADDED
//	(P1-E25-W5-S51-T3).

package ci

import (
	"context"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultWaitTimeout is wait-on-green's default budget (08-INIT-CONFIG-SPEC
// convention: 30 minutes) when WaitOptions.Timeout is unset.
const DefaultWaitTimeout = 30 * time.Minute

// DefaultWaitPollInterval is how often WaitOnGreen re-polls when
// WaitOptions.PollInterval is unset.
const DefaultWaitPollInterval = 15 * time.Second

// WaitOptions is one wait-on-green request.
type WaitOptions struct {
	Owner, Repo string
	// Ref is a branch name or a commit SHA; findRunForRef matches either.
	Ref string
	// RequiredChecks names the jobs that must succeed. Empty selects the
	// AUTO mode documented on evaluateChecks: every non-skipped job of a
	// run that has itself COMPLETED successfully.
	RequiredChecks []string
	// AllowSkipped names the required checks whose `skipped` conclusion
	// the caller accepts as green. Nothing else ever reads skipped as
	// green (D2): a required check that was skipped did not pass.
	AllowSkipped []string
	Timeout      time.Duration
	PollInterval time.Duration
}

// resolve applies the documented defaults to zero-valued fields.
func (o WaitOptions) resolve() WaitOptions {
	if o.Timeout <= 0 {
		o.Timeout = DefaultWaitTimeout
	}
	if o.PollInterval <= 0 {
		o.PollInterval = DefaultWaitPollInterval
	}
	return o
}

// validate refuses a request that names no repository or ref.
func (o WaitOptions) validate() error {
	if strings.TrimSpace(o.Owner) == "" || strings.TrimSpace(o.Repo) == "" {
		return cascade.New(cascade.KindInvalidInput, "ci: wait-on-green requires an owner and a repo")
	}
	if strings.TrimSpace(o.Ref) == "" {
		return cascade.New(cascade.KindInvalidInput, "ci: wait-on-green requires a ref (branch or head SHA)")
	}
	return nil
}

// ownerRepo renders the "owner/repo" string the never-pay guard routes by.
func (o WaitOptions) ownerRepo() string { return o.Owner + "/" + o.Repo }

// Sleeper waits d or returns ctx's cancellation error, whichever comes
// first. Declared as a seam so a test can prove timeout/cancellation
// behavior without a real sleep (Art.11).
type Sleeper func(ctx context.Context, d time.Duration) error

// ContextSleep is the production Sleeper: a context-aware real-time wait.
func ContextSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// WaitDeps carries WaitOnGreen's collaborators. All three are required.
type WaitDeps struct {
	Client *Client
	Clock  runtime.Clock
	Sleep  Sleeper
}

func (d WaitDeps) validate() error {
	if d.Client == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: wait-on-green requires a non-nil Client")
	}
	if d.Clock == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: wait-on-green requires a non-nil Clock")
	}
	if d.Sleep == nil {
		return cascade.New(cascade.KindInvalidInput, "ci: wait-on-green requires a non-nil Sleep")
	}
	return nil
}

// CheckStatus is one required check's last observed state.
type CheckStatus struct {
	Name       string
	Status     RunStatus
	Conclusion RunConclusion
}

// WaitResult is one WaitOnGreen call's outcome.
type WaitResult struct {
	Owner, Repo string
	Ref         string
	RunID       int64
	HeadSHA     string
	// Passed is true only on a successful, all-green return. MergeOnGreen's
	// rebindCheck refuses a WaitResult with Passed=false.
	Passed bool
	// Conclusion is the RUN's own conclusion as normalize.go resolved it
	// -- the value evaluateChecks raises KindConflict on when it is
	// anything but success, and the value P1-E25-W5-S51-T4's routing
	// decision reads (internal/ci/attention.go). ConclusionNone means the
	// run had not concluded when this result was produced.
	Conclusion RunConclusion
	// WorkflowName is the ACTIONS WORKFLOW name (normalize.go's Run.Name)
	// -- the subject a [ci.watch] `workflow` glob matches against. Job
	// names live in Checks, never here.
	WorkflowName string
	Checks       []CheckStatus
	Elapsed      time.Duration
}

// waitPlatformRefusal is the platform gate BOTH verbs consult before any
// poll (D3). It is a package-level seam for exactly one reason: the
// refusal is build-tag-selected (waitmerge_unix.go / waitmerge_windows.go),
// so a GOOS=windows-only call site is invisible to every other lane, and
// "the production path calls it" would be an unfalsifiable claim off
// Windows. A test replaces it to prove the call site is on the production
// path, and the windows lane still proves the real refusal's own text.
var waitPlatformRefusal = waitDaemonAbsentRefusal

// WaitOnGreen blocks until every required check on opts.Owner/opts.Repo at
// opts.Ref reports success, opts.Timeout expires, ctx is canceled, or a
// check concludes failure/cancelled/timed_out/unrecognized (fail-closed,
// 06 §5.20) -- whichever happens first.
func WaitOnGreen(ctx context.Context, deps WaitDeps, opts WaitOptions) (WaitResult, error) {
	if err := waitPlatformRefusal(); err != nil {
		return WaitResult{}, err
	}
	if err := deps.validate(); err != nil {
		return WaitResult{}, err
	}
	opts = opts.resolve()
	if err := opts.validate(); err != nil {
		return WaitResult{}, err
	}
	if err := guardActionsPoll(ctx, deps.Client.routes, opts.ownerRepo()); err != nil {
		return WaitResult{}, err
	}
	return deps.waitLoop(ctx, opts)
}

// waitLoop is WaitOnGreen's body once every precondition holds. Split out
// to keep both functions inside the 50-line cap.
func (d WaitDeps) waitLoop(ctx context.Context, opts WaitOptions) (WaitResult, error) {
	start := d.Clock.Now()
	deadline := start.Add(opts.Timeout)
	var lastJobs []Job
	var backoff time.Duration

	for {
		if err := ctx.Err(); err != nil {
			return WaitResult{}, cascade.Wrap(cascade.KindCanceled, err, "ci: wait-on-green canceled")
		}
		if !d.Clock.Now().Before(deadline) {
			return WaitResult{}, cascade.Newf(cascade.KindTimeout,
				"ci: wait-on-green timed out after %s waiting for %s@%s", opts.Timeout, opts.ownerRepo(), opts.Ref)
		}

		run, jobs, err := d.pollOnce(ctx, opts, lastJobs)
		switch {
		case err != nil && !isTransientPollError(err):
			return WaitResult{}, err
		case err != nil:
			backoff = nextPollBackoff(backoff, opts.PollInterval)
		default:
			backoff = 0
			if run.RunID != 0 {
				lastJobs = jobs
				result, resolved, decideErr := d.decide(opts, run, jobs, start)
				if decideErr != nil {
					return result, decideErr
				}
				if resolved {
					return result, nil
				}
			}
		}

		delay := opts.PollInterval
		if backoff > 0 {
			delay = backoff
		}
		if err := d.Sleep(ctx, delay); err != nil {
			return WaitResult{}, cascade.Wrap(cascade.KindCanceled, err, "ci: wait-on-green canceled while waiting")
		}
	}
}

// decide folds one poll's observation into a WaitResult.
func (d WaitDeps) decide(opts WaitOptions, run Run, jobs []Job, start time.Time) (WaitResult, bool, error) {
	resolved, checks, err := evaluateChecks(opts, run, jobs)
	result := WaitResult{
		Owner: opts.Owner, Repo: opts.Repo, Ref: opts.Ref,
		RunID: run.RunID, HeadSHA: run.HeadSHA, Checks: checks,
		Conclusion: run.Conclusion, WorkflowName: run.Name,
	}
	if err != nil {
		return result, false, err
	}
	if !resolved {
		return result, false, nil
	}
	result.Passed = true
	result.Elapsed = d.Clock.Now().Sub(start)
	return result, true, nil
}

// pollOnce fetches the current run (if any matches opts.Ref) and its jobs.
// A 304 Not Modified on either call reuses the last known jobs rather than
// treating "nothing changed" as "nothing exists".
func (d WaitDeps) pollOnce(ctx context.Context, opts WaitOptions, lastJobs []Job) (Run, []Job, error) {
	runsResult, err := d.Client.PollRuns(ctx, opts.Owner, opts.Repo)
	if err != nil {
		return Run{}, nil, err
	}
	if runsResult.NotModified {
		return Run{}, lastJobs, nil
	}
	run, ok := findRunForRef(runsResult.Runs, opts.Ref)
	if !ok {
		return Run{}, nil, nil
	}
	jobsResult, err := d.Client.PollJobs(ctx, opts.Owner, opts.Repo, run.RunID)
	if err != nil {
		return Run{}, nil, err
	}
	if jobsResult.NotModified {
		return run, lastJobs, nil
	}
	return run, jobsResult.Jobs, nil
}
