// Purpose: the retrieval_index doctor check's composition, split out of
// doctor.go so that file stays under the size cap — the same split
// cmd/cascade/daemon_unix_handlers.go already applies to buildRPCServer's
// registrations, for the same reason.
//
// SPORT: cmd/cascade/doctor (CHANGED, retrieval_index check, P1-E06-W2-S11-T4).
package main

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// buildRecallIndexManager lazily opens cascade.db and builds a
// lifecycle.Manager over it, for the retrieval_index doctor check. It
// must not block or error at registry-construction time (doctor.Check's
// contract), so the actual open happens inside the returned closure, run
// only when the check executes.
//
// FIX (DEFECT-doctor-retrieval-index-exclusive-lock.md): this used to call
// providers/sqlite.Open directly, which always attempts the §D-3 exclusive
// sidecar flock. Run inside `cascade doctor` while a daemon already holds
// that flock, the attempt failed with an opaque "exclusive lock held by
// another process" and the whole check reported StatusError — even though
// Verify (verify.go's own doc comment) is READ-ONLY and a live daemon is
// the single most common state to run `cascade doctor` in. This check
// never writes to the store (Run only ever calls Manager.Verify), so it
// now goes through internal/runtime.OpenEmbeddedReadStore — the same §D-3
// read-verb arbitration node_serve_presence.go/provider_health_cmd.go
// already use for the write side: it attempts the normal open first (the
// no-daemon-running case, unchanged), and ONLY on a flock/probe conflict
// (verified by TestWriteArbitration, internal/runtime/daemonless_test.go)
// falls back to a genuine read-only SQLite connection instead of failing
// — "a flock conflict is never a read failure" per that function's own
// doc comment. No socket probe is injected (nil, matching those two
// existing callers): the fallback triggers on the raw flock conflict
// alone, so this needs no daemon-liveness check of its own to be correct.
// retrieval.NewIndex and lifecycle.ManagerOptions.Store both accept the
// resulting provider.Store, whichever concrete connection it turned out
// to be, so nothing downstream of Open needs to know which path was
// taken.
func buildRecallIndexManager(paths runtime.PathProvider, clock runtime.Clock) lifecycle.ManagerBuilder {
	return func(ctx context.Context) (*lifecycle.Manager, func(), error) {
		dbPath := filepath.Join(paths.DataDir(), "cascade.db")
		store, closeStore, err := runtime.OpenEmbeddedReadStore(ctx, dbPath, nil)
		if err != nil {
			return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade doctor: open cascade.db")
		}
		closer := func() { _ = closeStore() }
		idx, err := retrieval.NewIndex(store)
		if err != nil {
			closer()
			return nil, nil, err
		}
		manager, err := lifecycle.NewManager(lifecycle.ManagerOptions{
			CatalogPath: filepath.Join(paths.DataDir(), "retrieval", "catalog.json"),
			Store:       store,
			Index:       idx,
			// Vectors: left nil, matching internal/daemon/recall_index.go's
			// production wiring — no embedding provider is configured at
			// this composition root, so this check must not construct a
			// vector store nothing ever writes into
			// (DEFECT-vector-leg-never-written-on-rebuild.md). Verify's
			// vectorIncomplete then correctly skips vector-completeness
			// checking instead of reporting a false incompleteness.
			Clock:    clock,
			TreeHash: doctorMarkerFunc(),
		})
		if err != nil {
			closer()
			return nil, nil, err
		}
		return manager, closer, nil
	}
}

// doctorMarkerFunc is what this check computes the generation marker
// with: internal/daemon's GitTreeHash, the SAME function the daemon's own
// recall.index.* handlers use.
//
// A named wiring point rather than an inline reference, so the test can
// assert what the check is actually pointed at. This file used to carry
// its own copy of the algorithm — the copy forgot to trim `git status
// --porcelain`, so on any dirty working tree `cascade doctor` and
// `cascade recall index verify` disagreed about whether the marker had
// drifted, and doctor exited 5 (R-14.278). There is one implementation
// now; if a second is ever wanted, this is the line that would have to
// change, and the test watches it.
func doctorMarkerFunc() lifecycle.GitTreeHashFunc { return daemon.GitTreeHash }
