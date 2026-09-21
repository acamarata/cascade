// Purpose: P1-E25-W5-S51-T4 -- the ci_results EVENT BUS subscriber this
// ticket's full_desc names. runner.go already publishes
// EventKindRunCompleted ("ci.run.completed") into the "ci_results"
// namespace through a real internal/events.Bus (runner.go's
// EventNamespace/publishCompleted); RouteCIResults consumes that
// subscription in the same Run(ctx, sub) shape internal/notify/router.go
// uses, and hands every failed run to attention.go's RouteFailure core --
// the same core `cascade github ci wait`'s operator-driven hook uses.
//
// WHAT THE PRODUCER CARRIES. EventKindRunCompleted describes a LOCAL
// gate run (`cascade ci run`): one run id, its repository, whether it
// passed, and the first failing step. This file therefore builds a
// candidate with ConclusionFailure, a single FailedJob naming the failed
// step, the "local" workflow name writeResult models a local run's single
// job as, an EMPTY ref (a local run has no GitHub ref) and an EMPTY run
// URL (a local run has no GitHub Actions URL -- inventing one would be a
// link to a run that does not exist). A GitHub-Actions-ingested run
// published on this same namespace by a future composition root routes
// through the identical path with all of those fields populated.
//
// WIRING GAP (recorded exactly like internal/daemon/attention_rpc.go's
// own precedent for supervision.NewSubscription, not papered over):
// RouteCIResults needs a daemon-lifetime context to cancel it, and
// buildRPCServer -- the composition root that would start it -- takes no
// ctx today. That is recorded as a testonly-allow.json entry naming
// internal/daemon/ci_attention_subscription.go as the eventual caller,
// with retire ticket P1-E25-W5-S51-T4, on the same terms the
// NewSubscription entry records the identical gap. Nothing here pretends
// the subscriber is started; the `cascade github ci wait` hook remains
// the second, operator-driven path, and both feed one core.
//
// Inputs: a live *events.Subscription on the ci_results namespace, a
// WatchSource re-read per event (the [ci.watch] key is hot, so a config
// edit takes effect without a restart), and an AttentionPusher.
// Outputs: nil when the subscription closes; ctx.Err() on cancellation;
// a fatal subscription error as-is. A per-event failure (undecodable
// payload, failing config read, failing push) is dropped with a slog WARN
// and the loop keeps running -- internal/notify/router.go's identical
// drop-with-WARN precedent, since one bad event must not silence every
// later one.
// Constraints: no bare time.Now; no panic on any payload (06 §5.20).
// SPORT: internal.ci.RouteCIResults/ADDED, internal.ci.WatchSource/ADDED
//
//	(P1-E25-W5-S51-T4).

package ci

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/acamarata/cascade/internal/events"
)

// LocalRunWorkflowName is the workflow name a LOCAL run presents to the
// [ci.watch] `workflow` glob. runner.go's writeResult models the whole
// local run as one job named "local"; this is that name, stated once.
const LocalRunWorkflowName = "local"

// WatchSource supplies the operator configuration each event is matched
// against. It is called per event rather than captured once so an edit to
// [ci.watch] takes effect on the next event, matching the key's hot
// (non-cold-section) status.
type WatchSource func(ctx context.Context) (RouteOptions, error)

// RouteCIResults reads sub.Events until it closes, ctx is canceled, or
// sub.Errs delivers a fatal subscription error (returned as-is). Every
// EventKindRunCompleted event describing a FAILED run is matched against
// the operator's watches and routed; every other Kind is ignored.
func RouteCIResults(ctx context.Context, sub *events.Subscription, watches WatchSource, pusher AttentionPusher) error {
	if sub == nil {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-sub.Errs:
			if ok && err != nil {
				return err
			}
		case ev, ok := <-sub.Events:
			if !ok {
				return nil
			}
			handleRunCompleted(ctx, ev, watches, pusher)
		}
	}
}

// handleRunCompleted routes one bus event, dropping (with a WARN) rather
// than failing the loop on anything it cannot act on.
func handleRunCompleted(ctx context.Context, ev events.Event, watches WatchSource, pusher AttentionPusher) {
	if ev.Kind != EventKindRunCompleted {
		return
	}
	var payload runCompletedPayload
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		slog.Default().Warn("ci: dropping an undecodable ci.run.completed payload", "error", err)
		return
	}
	candidate, ok := candidateFromRunCompleted(payload)
	if !ok {
		return
	}
	if watches == nil {
		return
	}
	opts, err := watches(ctx)
	if err != nil {
		slog.Default().Warn("ci: attention routing could not read the watch config", "error", err)
		return
	}
	if _, err := RouteFailure(ctx, pusher, opts, candidate); err != nil {
		slog.Default().Warn("ci: routing a ci_results failure to attention failed",
			"repo", candidate.Repo, "run_id", candidate.RunID, "error", err)
	}
}

// candidateFromRunCompleted builds the routing candidate for one
// completed-run event. A passing run, a run with no id, and a run with no
// repository identity are all "nothing to route" -- never a guess.
func candidateFromRunCompleted(p runCompletedPayload) (AttentionCandidate, bool) {
	if p.Passed || p.RunID == 0 || p.Repo == "" {
		return AttentionCandidate{}, false
	}
	var jobs []FailedJob
	if p.FailedStep != "" {
		jobs = []FailedJob{{Name: string(p.FailedStep), Conclusion: ConclusionFailure}}
	}
	return AttentionCandidate{
		Repo:       p.Repo,
		Workflow:   LocalRunWorkflowName,
		RunID:      p.RunID,
		Conclusion: ConclusionFailure,
		FailedJobs: jobs,
		DeepLink:   attentionDeepLink(p.Repo, p.RunID),
	}, true
}
