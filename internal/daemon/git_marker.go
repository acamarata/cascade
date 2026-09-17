package daemon

// Purpose: THE git generation marker (R-21.189) — the one implementation
// of "what tree is this index built from", used by the daemon's
// recall.index.* handlers and by `cascade doctor`'s retrieval_index check
// alike.
//
// Inputs: the process's working directory, read through the real git
// binary (Art.2 external contract, R-14.80).
// Outputs: "<commit>:<digest of the uncommitted changes>", or "" when
// there is no repository — a supported configuration, not an error.
//
// Constraints: THERE IS EXACTLY ONE OF THESE. `cascade doctor` used to
// carry its own copy, under a comment asserting the algorithm was
// identical to the daemon's. It was not: the copy hashed `git status
// --porcelain`'s output WITHOUT trimming it, so on any working tree with
// an uncommitted change the two digests differed and the two surfaces
// disagreed out loud — `cascade recall index verify` reported the marker
// current while `cascade doctor` reported it drifted and exited 5. Both
// halves agree on an EMPTY status, which is every test fixture and every
// CI checkout, so nothing in the suite could see it (R-14.278). If a
// second caller needs this, it imports this function; it does not copy it.
//
// SPORT: internal/daemon (CHANGED — one tree-hash function, P1-E19-W4-S42-T8).

import (
	"context"

	"github.com/acamarata/cascade/internal/retrieval"
)

// GitTreeHash is the production lifecycle.GitTreeHashFunc: the current
// commit plus a stable digest of the working tree's uncommitted changes,
// so an edit changes the marker even before it is committed.
//
// Exported because `cascade doctor` computes the same marker and must get
// the same answer. Falls back to the empty string on any git failure (no
// repository, git absent) rather than erroring — an install with no git
// repository is a supported configuration, and the marker then always
// reads DRIFTED, which is the documented fail-closed behavior for an
// unresolvable marker.
func GitTreeHash(ctx context.Context) (string, error) {
	head, err := recallIndexRunGit(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", nil //nolint:nilerr // no repository is a supported, not an error, configuration
	}
	status, err := recallIndexRunGit(ctx, "status", "--porcelain")
	if err != nil {
		status = ""
	}
	return head + ":" + retrieval.ChunkID([]byte(status)), nil
}
