// Purpose: the three collaborators context_slice_hook.go needs from the
//
//	rest of this package — the resolved [context.hydration] settings, the
//	scope label the capsule's header names, and the degraded-event write
//	the doctor check counts (P1-E16-W4-S34-T4).
//
// Inputs: the hook's own deps plus the payload it decoded.
// Outputs: settings, a label, and a best-effort event.
// Constraints: every function here degrades to a usable answer rather
//
//	than to an error. The hook has one job, it runs inside a 3-second
//	budget, and a config file it could not read is not a reason to leave
//	a prompt unhydrated when the defaults are perfectly serviceable.
//
// SPORT: cmd/cascade/context-slice-hook (ADD) — P1-E16-W4-S34-T4.
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/hydration"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// hydrationSettings resolves [context.hydration].
//
// An unreadable or malformed config yields the defaults rather than a
// refusal. That is the opposite of the loader's own posture, deliberately:
// the LOADER must refuse a malformed section so `cascade config` tells the
// user their file is wrong, but this hook is not the place a user learns
// it. Running with the documented defaults for one prompt is strictly
// better than that prompt losing its context to a typo in an unrelated
// table.
func hydrationSettings(deps contextScopeDeps) hydration.Config {
	cfg, err := loadRuntimeConfigFor(deps)
	if err != nil {
		return hydration.Default()
	}
	resolved, err := hydration.LoadSection(cfg.Extra[hydration.ParentSectionName])
	if err != nil {
		return hydration.Default()
	}
	return resolved
}

// hydrationScopeLabel renders the capsule header's "<kind>/<repository or
// general>" field.
//
// A scope that cannot be resolved renders as general, which is what an
// unresolved cwd IS — scope.ScopeKindGeneral is the resolver's own name
// for it, not a placeholder invented here.
func hydrationScopeLabel(ctx context.Context, deps contextScopeDeps, payload hookPayload) string {
	resolved, err := fetchContextScopeShow(ctx, deps, scope.ShowParams{
		Cwd: payload.Cwd, Session: payload.SessionID,
	})
	if err != nil {
		return string(scope.ScopeKindGeneral) + "/general"
	}
	name := "general"
	if resolved.Repository != nil && resolved.Repository.RootPath != "" {
		name = baseName(resolved.Repository.RootPath)
	}
	kind := resolved.Kind
	if kind == "" {
		kind = scope.ScopeKindGeneral
	}
	return string(kind) + "/" + name
}

// recordHydrationDegraded records one degraded hydration, best-effort.
//
// It routes the way every other daemonless-capable verb in this package
// routes: through the daemon when one is live, and directly onto the
// event bus otherwise. The split is not a preference here, it is a
// requirement — the store takes an EXCLUSIVE lock, so a hook that opened
// it while the daemon held it would not merely fail, it would fail in
// exactly the common case.
//
// Telemetry, never the hydration path's own correctness: every failure on
// either branch is dropped. The alternative is a hook that turns "we
// could not tell you hydration is degraded" into a second, louder failure
// on top of the first.
func recordHydrationDegraded(ctx context.Context, deps contextScopeDeps, reason string) {
	if publishDegradedViaDaemon(ctx, deps, reason) {
		return
	}
	publishDegradedEmbedded(ctx, deps, reason)
}

// publishDegradedViaDaemon dials the daemon and reports whether it
// answered. A daemonless probe that says "embedded", an unresolvable
// socket, or any RPC failure all report false so the caller falls back.
func publishDegradedViaDaemon(ctx context.Context, deps contextScopeDeps, reason string) bool {
	if st, ok := runtime.DaemonlessStateFrom(ctx); !ok || st.Embedded {
		return false
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return false
	}
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextAssembleDialTimeout)
	var result daemon.ContextHydrationDegradedResult
	return c.Do(ctx, daemon.ContextHydrationDegradedMethod,
		daemon.ContextHydrationDegradedParams{Reason: reason}, &result) == nil
}

// publishDegradedEmbedded opens the store, publishes, and closes it
// again.
//
// The close matters: this process is a hook the harness spawned and will
// reap, and a store left open holds an exclusive lock that the next thing
// to want the database — a daemon starting, another hook — would block
// on. A store it cannot open is a no-op, which on this path is almost
// always "the daemon has it", and the daemon branch above already tried.
func publishDegradedEmbedded(ctx context.Context, deps contextScopeDeps, reason string) {
	if deps.Paths == nil {
		return
	}
	payload, err := json.Marshal(daemon.ContextHydrationDegradedParams{Reason: reason})
	if err != nil {
		return
	}
	store, err := sqlite.Open(ctx, filepath.Join(deps.Paths.DataDir(), "cascade.db"))
	if err != nil {
		return
	}
	defer func() { _ = store.Close() }()
	hydration.PublishDegraded(ctx, store, payload)
}

// loadRuntimeConfigFor loads config.toml for deps, the same way every
// other command in this package does.
func loadRuntimeConfigFor(deps contextScopeDeps) (*runtime.Config, error) {
	if deps.Paths == nil {
		return nil, cascade.New(cascade.KindUnavailable, "cascade context slice: no config path is resolved")
	}
	return runtime.Load(context.Background(), runtime.LoadOptions{
		Path:    deps.Paths.ConfigPath(),
		Getenv:  deps.Getenv,
		Environ: deps.Environ,
	})
}

// baseName returns the last path element.
//
// It handles both separators rather than deferring to path/filepath,
// because the value is a repository root a scope record carries and a
// record written on one platform can be read on another. What the capsule
// header wants is the directory's own name, whichever way it was spelled.
func baseName(path string) string {
	trimmed := strings.TrimRight(path, "/\\")
	if i := strings.LastIndexAny(trimmed, "/\\"); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	if trimmed == "" {
		return "general"
	}
	return trimmed
}
