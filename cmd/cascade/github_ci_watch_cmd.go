// Purpose: `cascade github ci watch add|list|remove` (P1-E25-W5-S51-T4) --
// the CLI surface over [ci.watch] (internal/runtime/config_ci_watch*.go)
// -- and routeWaitFailureToAttention, the real production call site that
// turns a `cascade github ci wait` failure into an R/S-39.T1 attention
// item. Split from github_ci_cmd.go purely to keep that file under
// Art.10.3's 300-line cap (it sits at it), moving hostedProcessVerbs here
// with it since both are about the SAME three new verbs.
//
// Inputs: cobra args/flags; the resolved config.toml path (lazyPaths, the
// same deferred PathProvider adapter every other config-touching command
// in this package already uses); the runtime store, opened the SAME way
// hostMergeOnGreen's policy engine already does (openMCPPolicyStore,
// github_ci_cmd.go), for the attention push alone.
// Outputs: process output via cmd.Printf, matching newGitHubCIWaitCmd's
// own plain-text style (no --json support is added here); a typed
// taxonomy error on failure.
// Constraints: attention routing is best-effort observability, never a
// gate on hostWaitOnGreen's own result (runner.go's identical "Events
// optional... never surfaced as a Run error" precedent) -- a routing
// failure is logged, never returned to the caller. `watch add` refuses on
// Windows tier-2 (AC6) through ci.PlatformRefusal, the SAME
// build-tag-split verdict WaitOnGreen's own gate consults, rather than a
// runtime.GOOS branch AND without opening the runtime store as a probe
// side effect.
// SPORT: cmd/cascade:github-ci-watch-verbs (ADD) -- P1-E25-W5-S51-T4.
package main

import (
	"context"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// hostedProcessVerbs is plugin_process_mount.go's host-implementation map.
func hostedProcessVerbs() map[string]func() *cobra.Command {
	bridge := productionGitHubCIBridge()
	return map[string]func() *cobra.Command{
		"github-ci-wait":           func() *cobra.Command { return newGitHubCIWaitCmd(bridge) },
		"github.ci.merge-on-green": func() *cobra.Command { return newGitHubCIMergeOnGreenCmd(bridge) },
		"github-ci-watch-add":      newGitHubCIWatchAddCmd,
		"github-ci-watch-list":     newGitHubCIWatchListCmd,
		"github-ci-watch-remove":   newGitHubCIWatchRemoveCmd,
	}
}

// newGitHubCIWatchAddCmd builds `cascade github ci watch add <owner/repo>`.
func newGitHubCIWatchAddCmd() *cobra.Command {
	var branch, workflow string
	cmd := &cobra.Command{
		Use:   "add <owner/repo>",
		Short: "Watch a repository's CI results, routing failures to the attention queue",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := probeAttentionDaemonAvailable(); err != nil {
				return err
			}
			entry := runtime.CIWatchEntry{Repo: args[0], Branch: branch, Workflow: workflow}
			if err := entry.Validate(); err != nil {
				return err
			}
			return addCIWatchEntry(cmd, entry)
		},
	}
	cmd.Flags().StringVar(&branch, "branch", "", "only route failures on branches matching this glob (default: every branch)")
	cmd.Flags().StringVar(&workflow, "workflow", "",
		"only route failures of workflows whose name matches this glob (default: every workflow)")
	return cmd
}

// addCIWatchEntry does the actual load-upsert-write-report sequence,
// split out of newGitHubCIWatchAddCmd's RunE to keep it under the 50-line
// funlen cap. Idempotent: a second add for the same repo updates config,
// exits 0, and reports the delta (AC4) rather than appending a duplicate.
func addCIWatchEntry(cmd *cobra.Command, entry runtime.CIWatchEntry) error {
	path := lazyPaths{}.ConfigPath()
	cfg, err := runtime.Load(cmd.Context(), runtime.LoadOptions{Path: path})
	if err != nil {
		return err
	}
	entries, updated := runtime.UpsertCIWatchEntry(cfg.CIWatch.Entries, entry)
	if err := runtime.WriteCIWatchEntries(path, entries); err != nil {
		return err
	}
	verb := "added"
	if updated {
		verb = "updated"
	}
	cmd.Printf("%s watch for %s (branch=%q workflow=%q)\n", verb, entry.Repo, entry.Branch, entry.Workflow)
	return nil
}

// newGitHubCIWatchListCmd builds `cascade github ci watch list`.
func newGitHubCIWatchListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured [ci.watch] entries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := runtime.Load(cmd.Context(), runtime.LoadOptions{Path: lazyPaths{}.ConfigPath()})
			if err != nil {
				return err
			}
			if len(cfg.CIWatch.Entries) == 0 {
				cmd.Println("no watched repositories")
				return nil
			}
			for _, e := range cfg.CIWatch.Entries {
				cmd.Printf("%s  branch=%q  workflow=%q\n", e.Repo, e.Branch, e.Workflow)
			}
			return nil
		},
	}
}

// newGitHubCIWatchRemoveCmd builds `cascade github ci watch remove
// <owner/repo>`.
func newGitHubCIWatchRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <owner/repo>",
		Short: "Stop watching a repository's CI results",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := lazyPaths{}.ConfigPath()
			cfg, err := runtime.Load(cmd.Context(), runtime.LoadOptions{Path: path})
			if err != nil {
				return err
			}
			entries, removed := runtime.RemoveCIWatchEntry(cfg.CIWatch.Entries, args[0])
			if !removed {
				cmd.Printf("no-op: %s was not watched\n", args[0])
				return nil
			}
			if err := runtime.WriteCIWatchEntries(path, entries); err != nil {
				return err
			}
			cmd.Printf("removed watch for %s\n", args[0])
			return nil
		},
	}
}

// probeAttentionDaemonAvailable is `cascade github ci watch add`'s
// Windows tier-2 gate (AC6): the attention queue this feature routes
// toward is a daemon-owned domain, so add refuses on a platform that has
// no daemon rather than accepting a watch this build can never act on.
//
// It reads ci.PlatformRefusal -- the SAME build-tag-split verdict
// WaitOnGreen's own gate consults (waitmerge_unix.go /
// waitmerge_windows.go), never a runtime.GOOS branch -- and so is
// SIDE-EFFECT FREE: it does not open (and therefore does not create or
// migrate) the runtime store merely to learn the platform's answer. An
// IO or permission failure opening that store is NOT a platform verdict
// and is no longer relabelled as one: it surfaces verbatim from
// openAttentionPusher at routing time. list/remove stay available
// everywhere -- plain config-file operations with no daemon dependency.
func probeAttentionDaemonAvailable() error {
	if err := ci.PlatformRefusal(); err != nil {
		return cascade.Wrap(cascade.KindUnsupported, err,
			"cascade github ci watch add: the attention queue a watch routes into is daemon-owned, "+
				"and this platform has no daemon (Windows tier-2)")
	}
	return nil
}

// waitAndRoute is the ONE call site of the attention-routing hook. Both
// verbs that observe a CI conclusion reach it -- hostWaitOnGreen directly
// and hostMergeOnGreen with its own already-built WaitDeps (it needs that
// same client afterwards for ci.ClientHeadSHA, so it cannot call
// hostWaitOnGreen, which would build a second client whose ETag state is
// not the one the merge gate reads). Keeping the routing call here rather
// than in each verb is what stops the two copies drifting apart
// (P1-E25-W5-S51-T4, CR finding 3).
func waitAndRoute(ctx context.Context, deps ci.WaitDeps, opts ci.WaitOptions) (ci.WaitResult, error) {
	result, waitErr := ci.WaitOnGreen(ctx, deps, opts)
	routeWaitFailureToAttention(ctx, result, waitErr)
	return result, waitErr
}

// routeWaitFailureToAttention is hostWaitOnGreen's real production call
// site into ci.RouteWaitFailure (see internal/ci/attention.go's header
// note for why this file, not a bus subscriber, is that path). It is
// best-effort: a routing failure is logged and NEVER changes
// hostWaitOnGreen's own return value, matching internal/ci/runner.go's
// identical "Events optional ... never surfaced as a Run error"
// precedent -- attention routing is observability, not a gate.
func routeWaitFailureToAttention(ctx context.Context, result ci.WaitResult, waitErr error) {
	if kind, ok := cascade.KindOf(waitErr); !ok || kind != cascade.KindConflict {
		return
	}
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: lazyPaths{}.ConfigPath()})
	if err != nil || len(cfg.CIWatch.Entries) == 0 {
		return
	}
	opts := ci.RouteOptions{Watches: cfg.CIWatch.Entries, PrivateRepos: cfg.CIPolicy.PrivateRepos}
	pusher, closeStore, err := openAttentionPusher(opts.PrivateRepos)
	if err != nil {
		slog.Default().Warn("cascade github ci wait: attention routing unavailable", "error", err)
		return
	}
	defer closeStore()
	routed, err := ci.RouteWaitFailure(ctx, pusher, opts, result, waitErr)
	switch {
	case err != nil:
		slog.Default().Warn("cascade github ci wait: attention routing failed", "error", err)
	case routed.Routed:
		slog.Default().Info("cascade github ci wait: routed a CI failure to the attention queue",
			"repo", result.Owner+"/"+result.Repo, "run_id", result.RunID, "deep_link", routed.Candidate.DeepLink)
	case routed.Existing:
		slog.Default().Info("cascade github ci wait: this failed run is already in the attention queue",
			"repo", result.Owner+"/"+result.Repo, "run_id", result.RunID, "acked", routed.Acked)
	}
}

// openAttentionPusher opens the SAME runtime store hostMergeOnGreen's
// policy engine opens (openMCPPolicyStore) and hands it to
// newCIAttentionPusher with a nil EventBus: no SSE mirror fires for a
// CLI-process push, matching attention_store.go's own documented nil-bus
// no-SSE mode (emit() no-ops on a nil bus).
func openAttentionPusher(privateRepos []string) (ci.AttentionPusher, func(), error) {
	clock := runtime.NewSystemClock()
	store, closeStore, err := openMCPPolicyStore(lazyPaths{}, clock)
	if err != nil {
		return nil, nil, err
	}
	pusher, err := newCIAttentionPusher(store, clock, nil, privateRepos)
	if err != nil {
		closeStore()
		return nil, nil, err
	}
	return pusher, closeStore, nil
}

// newCIAttentionPusher builds the ONE production ci.Router shape this
// process uses: daemon.NewAttentionStore wrapping store -- the identical
// constructor RegisterFleetAttentionHandler (internal/daemon/attention_rpc.go)
// uses for the daemon's own RPC surface -- then hands that Store to
// ci.Router, so every push runs through supervision.RoutePush's data-class
// check rather than calling Store.Push directly.
//
// Shared by openAttentionPusher above (the CLI path, bus always nil) and
// daemon_unix_ci_attention.go's wireCIAttentionSubscription (the daemon
// path, over the daemon's own already-open store and its real bus, so a
// push there mirrors onto fleet.attention.changed like every other
// attention write) -- one construction, not two copies that could drift.
//
// Inputs: an already-open provider.Store, a Clock, an optional *events.Bus
// (nil disables the SSE mirror), and the [ci.policy.repos].private globs.
// Outputs: a ci.AttentionPusher, or a typed KindInternal error if store
// construction fails.
func newCIAttentionPusher(store provider.Store, clock runtime.Clock, bus *events.Bus, privateRepos []string) (ci.AttentionPusher, error) {
	attention := daemon.NewAttentionStore(store, clock, bus)
	if attention == nil {
		return nil, cascade.New(cascade.KindInternal, "cascade: ci attention store construction failed")
	}
	return ci.Router{Store: attention, Class: ci.NewCIDataClassChecker(privateRepos)}, nil
}
