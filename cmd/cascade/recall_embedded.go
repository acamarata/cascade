// Purpose: DEFECT-recall-no-embedded-path.md's fix. `cascade recall
//
//	<query>` printed "running in embedded (daemonless) mode" and then
//	dialed the daemon anyway (recall.go's recallCall never consulted
//	runtime.DaemonlessStateFrom), so every fresh install with no daemon
//	running failed with a dial error instead of an answer. This file adds
//	the SAME branch every other daemonless command already carries
//	(context_cmd.go's fetchContextSlice/fetchContextShow,
//	context_scope.go's fetchContextScopeShow, context_sync_cmd.go,
//	fleet.go): consult the probe once, dial the daemon only when it
//	confirmed one is live, and otherwise answer from the identical
//	recall.Service composition registerRecallHandler builds for the daemon
//	RPC handler (daemon_unix_handlers.go), invoked through
//	recall.Handler.Query itself — the same function value the daemon's
//	recall.query method dispatches to — so this file adds no second,
//	independently-maintained fusion or result-shaping path.
//
// Inputs: the recall.QueryParams RunE already built.
// Outputs: recall.QueryResult, or a scrubbed taxonomy error.
//
// Constraints: split from recall.go, which sits at its 300-line cap
//
//	(Art.10.3). No platform-specific imports (Art.5) — the FileCatalog
//	read and Service composition are identical on every OS.
//
// SPORT: cmd.cascade.cmd.recall (FIX, embedded-path routing).
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recallQuery routes recall.query through the client when the daemonless
// probe confirms a live daemon, and through the embedded runtime
// otherwise, mirroring fetchContextSlice's routing rule exactly
// (context_cmd.go). An undecidable probe result (DaemonlessStateFrom's
// ok=false, e.g. a caller that never ran root.go's PersistentPreRunE) is
// treated as embedded, matching root.go's own "unknown defaults to
// embedded" rule (context_scope.go's fetchContextScopeShow doc comment).
//
// recallCall already scrubs every diagnostic it returns (its own doc
// comment: "Scrubbing happens HERE, at the boundary"); the embedded branch
// is scrubbed here for the identical reason — a FileCatalog read error or
// an os.MkdirAll failure names the machine path it failed on, and that
// must never reach a terminal unredacted.
func recallQuery(cmd *cobra.Command, deps recallDeps, params recall.QueryParams) (recall.QueryResult, error) {
	ctx := cmd.Context()
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if ok && !st.Embedded {
		var result recall.QueryResult
		if err := recallCall(cmd, deps, recall.MethodQuery, params, &result); err != nil {
			return recall.QueryResult{}, err
		}
		return result, nil
	}
	result, err := resolveRecallEmbedded(ctx, deps, params)
	return result, scrubDiagnostic(err)
}

// resolveRecallEmbedded builds the identical recall.Service composition
// registerRecallHandler builds for the daemon (same FileCatalog path
// convention, same rrf.Params, same fusion.NewVectorLeg with no embedder
// configured), then calls recall.NewHandler(svc).Query — the exact
// function the daemon's RPC dispatcher calls — so the embedded and daemon
// paths can never disagree about what one recall means.
//
// The one deliberate difference from the daemon's own composition is the
// vector leg's event sink: nil here, which fusion.NewVectorLeg's doc
// comment names as "a caller that has no event bus to report the
// degradation to" — exactly a one-shot CLI invocation with no daemon-owned
// event log to publish the vector-leg-unavailable notice onto.
func resolveRecallEmbedded(ctx context.Context, deps recallDeps, params recall.QueryParams) (recall.QueryResult, error) {
	indexDir := recallIndexDir(deps.Paths)
	// FileCatalog.Load only reads (internal/retrieval/recall/catalog.go:
	// os.ReadFile), and already answers a missing directory exactly as it
	// answers a missing file — fs.ErrNotExist maps to KindNotFound "no
	// retrieval index has been built yet" either way — so this MkdirAll is
	// not required for THAT read to behave correctly on a virgin HOME. It
	// runs anyway so the directory a later `recall index rebuild` writes
	// into already exists, the same virgin-HOME bootstrap
	// resolveContextSliceEmbedded performs for cascade.db's parent
	// directory (FIX-context-slice-virgin-home.md), and matching
	// openRuntimeStore's (daemon_unix_store.go) own 0o700 choice for a
	// private data directory.
	if err := os.MkdirAll(indexDir, 0o700); err != nil {
		return recall.QueryResult{}, cascade.Wrap(cascade.KindUnavailable, err,
			"cascade recall: create retrieval index directory")
	}
	catalog := recall.NewFileCatalog(filepath.Join(indexDir, recall.CatalogFileName))
	svc, err := recall.NewService(catalog, rrf.Params{}, fusion.NewVectorLeg(nil, nil, nil))
	if err != nil {
		return recall.QueryResult{}, err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return recall.QueryResult{}, cascade.Wrap(cascade.KindInternal, err, "cascade recall: encode params")
	}
	out, err := recall.NewHandler(svc).Query(ctx, raw)
	if err != nil {
		return recall.QueryResult{}, err
	}
	result, ok := out.(recall.QueryResult)
	if !ok {
		return recall.QueryResult{}, cascade.Newf(cascade.KindInternal,
			"cascade recall: embedded handler returned %T, not recall.QueryResult", out)
	}
	return result, nil
}
