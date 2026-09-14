// Purpose: the retrieval_index doctor check's composition, split out of
// doctor.go so that file stays under the size cap — the same split
// cmd/cascade/daemon_unix_handlers.go already applies to buildRPCServer's
// registrations, for the same reason.
//
// SPORT: cmd/cascade/doctor (CHANGED, retrieval_index check, P1-E06-W2-S11-T4).
package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/localvector"
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
// retrieval.NewIndex, localvector.New and lifecycle.ManagerOptions.Store
// all accept the resulting provider.Store, whichever concrete connection
// it turned out to be, so nothing downstream of Open needs to know which
// path was taken.
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
			Vectors:     localvector.New(store),
			Clock:       clock,
			TreeHash:    doctorGitTreeHash,
		})
		if err != nil {
			closer()
			return nil, nil, err
		}
		return manager, closer, nil
	}
}

// doctorGitTreeHash is the doctor command's own copy of the production
// GitTreeHashFunc (identical algorithm to internal/daemon/recall_index.go's
// gitTreeHashExec: HEAD plus a digest of the working tree's uncommitted
// changes). Duplicated rather than exported from internal/daemon, since
// that package's os/exec use is scoped to its own composition-root files
// and this CLI-side check has no daemon to reach; both copies fail closed
// to "" on any git error, which verify's MarkerStatus reads as DRIFTED.
func doctorGitTreeHash(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	head, err := cmd.Output()
	if err != nil {
		return "", nil //nolint:nilerr // no repository is a supported, not an error, configuration
	}
	statusCmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	status, err := statusCmd.Output()
	if err != nil {
		status = nil
	}
	return strings.TrimSpace(string(head)) + ":" + retrieval.ChunkID(status), nil
}
