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
	"github.com/acamarata/cascade/providers/sqlite"
)

// buildRecallIndexManager lazily opens cascade.db (its own connection,
// separate from any daemon process — matching internal/daemon/
// context_scope.go's "own second connection" precedent) and builds a
// lifecycle.Manager over it, for the retrieval_index doctor check. It
// must not block or error at registry-construction time (doctor.Check's
// contract), so the actual sqlite.Open call happens inside the returned
// closure, run only when the check executes.
func buildRecallIndexManager(paths runtime.PathProvider, clock runtime.Clock) lifecycle.ManagerBuilder {
	return func(ctx context.Context) (*lifecycle.Manager, func(), error) {
		dbPath := filepath.Join(paths.DataDir(), "cascade.db")
		driver, err := sqlite.Open(ctx, dbPath)
		if err != nil {
			return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "cascade doctor: open cascade.db")
		}
		closer := func() { _ = driver.Close() }
		idx, err := retrieval.NewIndex(driver)
		if err != nil {
			closer()
			return nil, nil, err
		}
		manager, err := lifecycle.NewManager(lifecycle.ManagerOptions{
			CatalogPath: filepath.Join(paths.DataDir(), "retrieval", "catalog.json"),
			Store:       driver,
			Index:       idx,
			Vectors:     localvector.New(driver),
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
