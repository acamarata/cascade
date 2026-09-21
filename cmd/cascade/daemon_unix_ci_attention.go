//go:build !windows

// Purpose: P1-E25-W5-S51-T4's daemon composition-root call site for
// internal/ci.RouteCIResults -- the ci_results bus subscriber that routes
// a watched repository's failed run to fleet attention. Closes the gap
// internal/ci/attention_subscribe.go's own header and
// internal/build/testonly-allow.json's (now-removed) entry recorded: no
// composition root threaded a daemon-lifetime ctx to it.
//
// COMPOSITION ROOT CHOSEN. buildRPCServer (daemon_unix_run.go) is NOT that
// root: it takes no ctx, and giving it one would touch its production call
// site plus every cmd/cascade integration test that builds it directly --
// exactly the "nine call sites" gap internal/daemon/attention_rpc.go's own
// header already records for supervision.NewSubscription, and this ticket
// does not re-open it. wireBackgroundSubsystems (daemon_unix_reload.go),
// one file over, already IS such a root for its two existing subscribers
// (startFleetMetricsConsumer, jobs.Scheduler): it receives platformDaemonRun's
// real ctx and derives its own context.WithCancel child per subscriber
// (metricsCtx/schedCtx), folding each cancel into the *cleanup* func
// platformDaemonRun defers -- ctx itself is not guaranteed canceled when
// daemon.Run returns (that comment lives on wireBackgroundSubsystems), so a
// subscriber bound directly to it would outlive a clean shutdown. This
// subscriber joins that same pattern as a third child context, "ciCtx".
//
// Inputs: ciCtx (wireBackgroundSubsystems' own context.WithCancel(ctx)
// child), a WatchSource reading the hot-reloadable [ci.watch]/[ci.policy]
// keys off the daemon's *runtime.HotReloader (ciWatchSourceFromReloader
// below), the daemon's already-open provider.Store and real *events.Bus,
// an initial [ci.policy.repos].private snapshot for the pusher's
// data-class backstop (RouteOptions.PrivateRepos, read fresh per event via
// watches, is the live gate -- see attention_push.go's header), and the
// daemon's logger.
// Outputs: a running background goroutine consuming ci.EventNamespace
// until ciCtx is canceled, pushing through the SAME ci.Router shape
// github_ci_watch_cmd.go's openAttentionPusher uses (newCIAttentionPusher).
// Constraints: a pusher or subscription that cannot be constructed is
// LOGGED and the subscriber never starts -- startFleetMetricsConsumer's
// identical posture: an attention queue this daemon cannot reach is an
// observability gap, not a boot-fatal condition. No bare time.Now.
// SPORT: cmd/cascade/daemon (ADD), internal.ci.RouteCIResults/WIRED
//
//	(P1-E25-W5-S51-T4).
package main

import (
	"context"
	"log/slog"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// ciAttentionSubscriberCursor is this subscriber's durable cursor name,
// distinct from fleetMetricsCursor and schedulerCursorName -- Bus.Subscribe
// refuses two live subscriptions sharing one cursor.
const ciAttentionSubscriberCursor = "ci-attention"

// ciAttentionSubscriberBuffer bounds how far this subscriber may fall
// behind before its delivery goroutine blocks, matching
// fleetMetricsBuffer's sizing rationale (a burst absorber, not a queue
// depth guarantee).
const ciAttentionSubscriberBuffer = 64

// ciWatchSourceFromReloader builds a ci.WatchSource reading hr.Current()
// per call, matching config_ci_watch.go's and attention_subscribe.go's own
// documented "the [ci.watch] key is hot" contract: an operator's config
// edit is visible to the NEXT event this subscriber handles, no restart
// required.
func ciWatchSourceFromReloader(hr *runtime.HotReloader) ci.WatchSource {
	return func(context.Context) (ci.RouteOptions, error) {
		cfg := hr.Current()
		return ci.RouteOptions{Watches: cfg.CIWatch.Entries, PrivateRepos: cfg.CIPolicy.PrivateRepos}, nil
	}
}

// wireCIAttentionSubscription builds the shared attention pusher
// (newCIAttentionPusher, github_ci_watch_cmd.go) over store/clock/bus and
// starts internal/ci.RouteCIResults against ci.EventNamespace under ctx --
// the same shape startFleetMetricsConsumer (daemon_unix_metrics.go) uses
// for its own namespace, so this daemon's background-subscriber wiring
// reads as one consistent pattern rather than a special case.
//
// Factored out of wireBackgroundSubsystems specifically so it can be
// exercised directly by a test with a real bus and a real TempDir sqlite
// store, without constructing wireBackgroundSubsystems' full
// daemonDeps/cfg/LogProvider scaffolding.
func wireCIAttentionSubscription(
	ctx context.Context, watches ci.WatchSource, store provider.Store, clock runtime.Clock,
	bus *events.Bus, privateRepos []string, logger *slog.Logger,
) {
	if bus == nil {
		return
	}
	pusher, err := newCIAttentionPusher(store, clock, bus, privateRepos)
	if err != nil {
		logger.Error("ci attention: pusher unavailable", "error", err)
		return
	}
	sub, err := bus.Subscribe(ctx, ci.EventNamespace, ciAttentionSubscriberCursor, ciAttentionSubscriberBuffer)
	if err != nil {
		logger.Error("ci attention: ci_results subscription unavailable", "error", err)
		return
	}
	go func() {
		defer func() { _ = sub.Unsubscribe() }()
		if runErr := ci.RouteCIResults(ctx, sub, watches, pusher); runErr != nil {
			logger.Error("ci attention: ci_results stream ended", "error", runErr)
		}
	}()
}
