// Purpose: P1-E25-W5-S51-T4 -- the CI failure-to-attention routing CORE.
// TWO real producers feed exactly one decision path:
//
//   - RouteCIResults (attention_subscribe.go) is the ci_results event-bus
//     subscriber. runner.go publishes EventKindRunCompleted into the
//     "ci_results" namespace through a real internal/events.Bus
//     (runner.go's EventNamespace/EventKindRunCompleted/publishCompleted),
//     and RouteCIResults consumes it in the same Run(ctx, sub) shape
//     internal/notify/router.go uses.
//   - RouteWaitFailure (below) is the operator-driven path, called from
//     cmd/cascade/github_ci_watch_cmd.go's routeWaitFailureToAttention on
//     every `cascade github ci wait` AND `merge-on-green` result (both
//     reach it through the single waitAndRoute hook in
//     github_ci_watch_cmd.go, never two copies of the routing call).
//
// Both build an AttentionCandidate and hand it to RouteFailure -- the ONE
// place a watch is matched, an item is shaped, and a push is made.
//
// ROUTING DECISION (run-level, not per-job): a run routes when its own
// conclusion is anything other than `success`, and never when the run has
// not concluded at all (ConclusionNone). That is exactly the rule
// waitmerge_checks.go's evaluateChecks already applies when it raises
// KindConflict ("run %d concluded %s" for every non-success conclusion),
// so a startup_failure run with zero jobs, and a run concluded `failure`
// whose individual jobs are all success/skipped, both route. An
// unrecognised conclusion routes too (06 §5.20 fail-closed: tell the
// operator rather than drop the failure silently).
//
// RECORDED LIMITATION (not papered over): normalize.go's knownConclusions
// is a closed allow-list, so GitHub's action_required/startup_failure/
// stale/neutral are already collapsed to ConclusionUnknown before any
// WaitResult exists. The candidate carries that value VERBATIM rather than
// remapping it to `failure`; recovering GitHub's raw string needs a
// normalize.go/Run change this ticket does not make.
//
// Inputs: an AttentionCandidate, the operator's RouteOptions ([ci.watch]
// entries plus [ci.policy.repos] private patterns), and an AttentionPusher
// (production: ci.Router over a real *supervision.Store, attention_push.go).
// Outputs: a RouteResult stating whether the item was NEWLY queued
// (Routed), already present (Existing), and already acknowledged (Acked);
// a non-nil error only for a genuine push or authorization refusal, which
// is surfaced to the operator, never swallowed.
// Constraints: glob matching is path.Match, never a hand-rolled substring
// check; the attention item's SourceRef is the run's deterministic
// IDENTITY (`ci:<repo>:<run_id>`), which is also attention_store.go's
// (Kind, SourceRef) dedup key -- one item per failed run, whatever changes
// between two observations of it.
// SPORT: internal.ci.RouteFailure/ADDED, internal.ci.RouteWaitFailure/ADDED,
//
//	internal.ci.BuildCandidate/ADDED, internal.ci.RouteResult/ADDED,
//	internal.ci.RouteOptions/ADDED, internal.ci.AttentionPusher/ADDED,
//	internal.ci.AttentionLister/ADDED, internal.ci.AttentionCandidate/ADDED
//	(P1-E25-W5-S51-T4).

package ci

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strconv"

	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
)

// attentionSourceRefPrefix marks an attention item this package pushed.
// SourceRef is documented by supervision.AttentionItem as "the job/session
// this item is about ... interpreted by whichever subsystem pushed it" --
// a REFERENCE, not a payload, which is why the run's full detail travels
// on RouteResult.Candidate (and in the ci_run/ci_job rows keyed by the
// same run_id) rather than inside this string.
const attentionSourceRefPrefix = "ci:"

// FailedJob is one failing check's name and conclusion.
type FailedJob struct {
	Name       string        `json:"name"`
	Conclusion RunConclusion `json:"conclusion"`
}

// AttentionCandidate is the fully-populated shape AC1 names: repo, ref,
// run_id, conclusion, failed_jobs, and the GitHub Actions run URL, plus
// the workflow name the [ci.watch] `workflow` glob matches against.
type AttentionCandidate struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref"`
	// Workflow is the ACTIONS WORKFLOW name (normalize.go's Run.Name),
	// carried on WaitResult.WorkflowName -- the subject the `workflow`
	// glob matches. Individual job names appear only in FailedJobs.
	Workflow   string        `json:"workflow"`
	RunID      int64         `json:"run_id"`
	Conclusion RunConclusion `json:"conclusion"`
	FailedJobs []FailedJob   `json:"failed_jobs"`
	// RunURL is computed from Repo/RunID -- no external call is made
	// (full_desc), and no URL column exists in the ci_results domain
	// (domain.go's runTableStep) to read instead. Empty for a LOCAL run,
	// which has no GitHub Actions URL at all.
	RunURL string `json:"run_url"`
	// DeepLink is this run's cascade:// URI, the opaque form
	// internal/notify/notify.go's DeepLink field documents and
	// docs/architecture.md:194 describes. Exact format:
	// cascade://ci/<owner>/<repo>/runs/<run_id>.
	DeepLink string `json:"deep_link"`
}

// githubActionsRunURL renders the canonical run URL, computed, never
// fetched.
func githubActionsRunURL(repo string, runID int64) string {
	return fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, runID)
}

// attentionDeepLink renders this candidate's cascade:// URI.
func attentionDeepLink(repo string, runID int64) string {
	return fmt.Sprintf("cascade://ci/%s/runs/%d", repo, runID)
}

// isRoutableConclusion reports whether a RUN conclusion is a failure the
// operator should be told about: anything that is not `success`, once the
// run has actually concluded.
func isRoutableConclusion(c RunConclusion) bool {
	return c != ConclusionSuccess && c != ConclusionNone
}

// failedJobs lists every check carrying a non-success, concluded result,
// SORTED by (name, conclusion) so two observations of one run produce the
// same list whatever order a paginated PollJobs returned them in.
func failedJobs(checks []CheckStatus) []FailedJob {
	var out []FailedJob
	for _, c := range checks {
		if !isRoutableConclusion(c.Conclusion) {
			continue
		}
		out = append(out, FailedJob{Name: c.Name, Conclusion: c.Conclusion})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Conclusion < out[j].Conclusion
	})
	return out
}

// BuildCandidate builds the routing candidate from one WaitOnGreen result.
// ok is false when the run did not conclude, concluded success, or carries
// no run id -- there is then nothing to route (06 §5.20: no basis, no
// routing). A routable run with ZERO failed jobs still routes: the run
// itself failed.
func BuildCandidate(result WaitResult) (AttentionCandidate, bool) {
	if result.RunID == 0 || !isRoutableConclusion(result.Conclusion) {
		return AttentionCandidate{}, false
	}
	full := result.Owner + "/" + result.Repo
	return AttentionCandidate{
		Repo: full, Ref: result.Ref, Workflow: result.WorkflowName,
		RunID: result.RunID, Conclusion: result.Conclusion,
		FailedJobs: failedJobs(result.Checks),
		RunURL:     githubActionsRunURL(full, result.RunID),
		DeepLink:   attentionDeepLink(full, result.RunID),
	}, true
}

// globMatch reports whether pattern matches subject (path.Match semantics
// -- the same call config_ci_watch.go's validator and
// plugins/github/cipolicy's resolver use). An empty pattern matches
// everything.
func globMatch(pattern, subject string) bool {
	if pattern == "" {
		return true
	}
	ok, err := path.Match(pattern, subject)
	return err == nil && ok
}

// matchesWatch reports whether any entry in watches matches (repo, branch,
// workflow).
func matchesWatch(watches []runtime.CIWatchEntry, repo, branch, workflow string) bool {
	for _, w := range watches {
		if globMatch(w.Repo, repo) && globMatch(w.Branch, branch) && globMatch(w.Workflow, workflow) {
			return true
		}
	}
	return false
}

// attentionSourceRef renders the run's deterministic identity, which is
// also attention_store.go's (Kind, SourceRef) dedup key: two observations
// of the same failed run -- even with a different failed-job set, a
// different job ORDER, or a re-run -- collapse onto ONE item.
func attentionSourceRef(c AttentionCandidate) string {
	return attentionSourceRefPrefix + c.Repo + ":" + strconv.FormatInt(c.RunID, 10)
}

// AttentionPusher pushes one item into the R/S-39.T1 attention queue. Its
// signature matches *supervision.Store.Push exactly; production passes
// ci.Router (attention_push.go), which routes the same call through
// supervision.RoutePush so the data-class check runs.
type AttentionPusher interface {
	Push(ctx context.Context, item supervision.AttentionItem) (supervision.AttentionItem, error)
}

// AttentionLister is the OPTIONAL second half of the queue seam: the
// existence probe RouteResult.Existing/Acked need. *supervision.Store and
// ci.Router both satisfy it; a pusher that does not is simply never asked,
// and Existing then stays false (documented, never guessed).
type AttentionLister interface {
	ListInScopes(ctx context.Context, scopes []supervision.ScopeRef, f supervision.Filter) ([]supervision.AttentionItem, error)
}

// RouteOptions is the operator configuration one routing decision reads.
type RouteOptions struct {
	// Watches are the [ci.watch] entries (config_ci_watch.go).
	Watches []runtime.CIWatchEntry
	// PrivateRepos are the [ci.policy.repos].private glob patterns
	// (config_ci.go). A candidate whose repo matches one is filed at
	// project scope rather than global -- see attention_push.go.
	PrivateRepos []string
}

// RouteResult reports what one routing decision actually did. Routed is
// true ONLY for an item this call newly queued: an item that was already
// in the queue (acknowledged or not) reports Existing, never Routed, so a
// caller can never log "routed a failure" for a push that queued nothing.
type RouteResult struct {
	Item      supervision.AttentionItem
	Candidate AttentionCandidate
	Routed    bool
	Existing  bool
	Acked     bool
}

// RouteFailure is the single routing core both producers call.
func RouteFailure(
	ctx context.Context, pusher AttentionPusher, opts RouteOptions, candidate AttentionCandidate,
) (RouteResult, error) {
	if pusher == nil || len(opts.Watches) == 0 || candidate.RunID == 0 || candidate.Repo == "" {
		return RouteResult{}, nil
	}
	if !matchesWatch(opts.Watches, candidate.Repo, candidate.Ref, candidate.Workflow) {
		return RouteResult{}, nil
	}
	item := supervision.AttentionItem{
		Kind:      supervision.KindError,
		SourceRef: attentionSourceRef(candidate),
		ScopeRef:  candidateScope(candidate, opts.PrivateRepos),
	}
	existing, acked := probeExisting(ctx, pusher, item)
	pushed, err := pusher.Push(ctx, item)
	if err != nil {
		return RouteResult{}, err
	}
	return RouteResult{
		Item: pushed, Candidate: candidate,
		Routed: !existing, Existing: existing, Acked: acked,
	}, nil
}

// probeExisting reports whether item's (Kind, SourceRef) identity is
// already queued, and whether it has been acknowledged. A pusher that is
// not an AttentionLister, or a failing list, answers "not existing": the
// probe refines the REPORT, it is never the thing that prevents a
// duplicate (attention_store.go's own dedup is).
func probeExisting(ctx context.Context, pusher AttentionPusher, item supervision.AttentionItem) (bool, bool) {
	lister, ok := pusher.(AttentionLister)
	if !ok {
		return false, false
	}
	queued, err := lister.ListInScopes(ctx, []supervision.ScopeRef{item.ScopeRef}, supervision.Filter{IncludeAcked: true})
	if err != nil {
		return false, false
	}
	for _, q := range queued {
		if q.Kind == item.Kind && q.SourceRef == item.SourceRef {
			return true, q.Acked()
		}
	}
	return false, false
}

// RouteWaitFailure is the operator-driven producer. It routes ONLY when
// waitErr carries cascade.KindConflict -- WaitOnGreen's own taxonomy for
// "the RUN concluded something other than success" (waitmerge_checks.go's
// evaluateChecks) -- as opposed to KindTimeout (the WAIT LOOP gave up, not
// the run), KindCanceled, or KindUnsupported (Windows tier-2), none of
// which is a CI conclusion at all.
func RouteWaitFailure(
	ctx context.Context, pusher AttentionPusher, opts RouteOptions, result WaitResult, waitErr error,
) (RouteResult, error) {
	if !isCIConclusionError(waitErr) {
		return RouteResult{}, nil
	}
	candidate, ok := BuildCandidate(result)
	if !ok {
		return RouteResult{}, nil
	}
	return RouteFailure(ctx, pusher, opts, candidate)
}
