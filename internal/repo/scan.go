package repo

// Purpose: scan orchestration -- git-root identity, all detectors, layout/
//   CI/harness facts, scope-graph membership resolution, and persistence.
// Constraints (contract/tree contradiction, full quote in the journal):
//   the ticket's HOW step 5 says scan.go resolves "git-root anchor +
//   remote URL via the REAL git binary", which reads as a direct os/exec
//   call. internal/build/egress_allow.go's EgressExecNormative/
//   NotYetMigrated lists do not include internal/repo, and this ticket's
//   files_scope.change does not include egress_allow.go -- adding an
//   entry there would be an unscoped edit into a file another agent may
//   be editing concurrently (AGENT-BRIEF: "touch ONLY your contract's
//   files_scope"). internal/context's own precedent for this exact
//   situation (discover.go's gitRoot) is to keep the os/exec call OUT of
//   this package entirely: internal/context/scope.GitRootFunc is an
//   injected seam, with the real implementation living at the
//   composition root, which the allowlist already admits. Scan follows
//   that precedent: it takes GitRootFunc and RemoteURLFunc seams; no
//   os/exec import exists in this file. scan_test.go supplies REAL
//   implementations (a genuine `git` subprocess against a t.TempDir()
//   repo, per Art.2) -- production non-test files are the only ones the
//   egress scan inspects (internal/build/egress_scan.go's own doc
//   comment), so this satisfies Art.2 without an allowlist edit.
// SPORT: repo/scan-orchestration/ADD (P1-E33-W7-S67-T1).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// GitRootFunc resolves cwd's git root, matching
// internal/context/scope.GitRootFunc's contract exactly (falls back to
// cwd on any failure, never an error).
type GitRootFunc func(ctx context.Context, cwd string) string

// RemoteURLFunc resolves root's configured "origin" remote URL, or ""
// when the repository has no remote (a purely local repository is not an
// error).
type RemoteURLFunc func(ctx context.Context, root string) string

// Clock abstracts time.Now for ScannedAt (A-T4); production callers pass
// internal/runtime.NewSystemClock(), tests pass a fixed value.
type Clock interface {
	Now() int64 // Unix seconds
}

// ScanDeps carries Scan's injected dependencies.
type ScanDeps struct {
	Store     *Store
	Graph     *scope.GraphStore
	GitRoot   GitRootFunc
	RemoteURL RemoteURLFunc
	Clock     Clock
}

func validateScanDeps(deps ScanDeps) error {
	switch {
	case deps.Store == nil:
		return cascade.New(cascade.KindInvalidInput, "repo: Scan requires a non-nil Store")
	case deps.Graph == nil:
		return cascade.New(cascade.KindInvalidInput, "repo: Scan requires a non-nil GraphStore")
	case deps.GitRoot == nil:
		return cascade.New(cascade.KindInvalidInput, "repo: Scan requires a non-nil GitRoot")
	case deps.RemoteURL == nil:
		return cascade.New(cascade.KindInvalidInput, "repo: Scan requires a non-nil RemoteURL")
	case deps.Clock == nil:
		return cascade.New(cascade.KindInvalidInput, "repo: Scan requires a non-nil Clock")
	}
	return nil
}

// Scan anchors cwd to its git root, derives repository identity, runs
// every detector plus the layout/CI/harness facts, resolves scope-graph
// membership, and persists the resulting Inventory. It is
// deterministic: two calls against an unchanged tree (and an unchanged
// scope graph) produce byte-identical Inventory values apart from
// ScannedAt.
//
// No production caller exists yet: this ticket's BOUNDARIES section
// explicitly excludes a CLI/RPC surface ("`context generate` mounts on
// AG/S-68.T5 ... nothing here"). internal/build/testonly-allow.json
// carries the corresponding entry naming AG/S-68.T5 as the ticket
// expected to wire this call, matching internal/jobs.NewStore's identical
// precedent (that package's own composition-root caller is likewise a
// later ticket).
func Scan(ctx context.Context, deps ScanDeps, cwd string) (Inventory, error) {
	if err := validateScanDeps(deps); err != nil {
		return Inventory{}, err
	}
	root := deps.GitRoot(ctx, cwd)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Inventory{}, cascade.Wrapf(cascade.KindInvalidInput, err, "repo: resolve absolute root for %q", root)
	}
	absRoot = filepath.Clean(absRoot)

	repoRef, err := resolveRepository(ctx, deps, absRoot)
	if err != nil {
		return Inventory{}, err
	}

	languages, err := DetectAll(ctx, absRoot)
	if err != nil {
		return Inventory{}, err
	}
	layout, err := ScanLayout(absRoot)
	if err != nil {
		return Inventory{}, err
	}
	ci, err := ScanCI(absRoot)
	if err != nil {
		return Inventory{}, err
	}
	harness, err := ScanHarness(absRoot)
	if err != nil {
		return Inventory{}, err
	}
	membership, err := resolveMembership(ctx, deps, repoRef)
	if err != nil {
		return Inventory{}, err
	}

	inv := Inventory{
		Repository: repoRef,
		Languages:  languages,
		Layout:     layout,
		CI:         ci,
		Harness:    harness,
		Membership: membership,
		ScannedAt:  deps.Clock.Now(),
	}
	if err := deps.Store.Upsert(ctx, inv); err != nil {
		return Inventory{}, err
	}
	return inv, nil
}

// resolveRepository looks up the repository already bound to absRoot in
// the scope graph, or registers a new one deterministically identified by
// remote+path-hash (never a random id -- that would break the
// determinism guarantee on a tree the scope graph has not seen yet).
func resolveRepository(ctx context.Context, deps ScanDeps, absRoot string) (RepositoryRef, error) {
	if existing, ok, err := deps.Graph.RepositoryForRoot(ctx, absRoot); err != nil {
		return RepositoryRef{}, err
	} else if ok {
		return RepositoryRef{ID: existing.ID, Remote: existing.Remote, PathHash: existing.PathHash}, nil
	}

	remote := deps.RemoteURL(ctx, absRoot)
	pathHash := hashPath(absRoot)
	id := repositoryID(remote, pathHash)

	if err := deps.Graph.PutRepository(ctx, scope.RepositoryRecord{ID: id, Remote: remote, PathHash: pathHash}); err != nil {
		return RepositoryRef{}, err
	}
	if err := deps.Graph.PutRepoPath(ctx, scope.RepoPathRecord{RootPath: absRoot, RepositoryID: id}); err != nil {
		return RepositoryRef{}, err
	}
	return RepositoryRef{ID: id, Remote: remote, PathHash: pathHash}, nil
}

// resolveMembership reads the scope graph's parent chain for repo as a
// project node, per R-16.3's resolution order. internal/repo persists
// this as read-only evidence; it never writes a scope/scope_edge row
// itself.
func resolveMembership(ctx context.Context, deps ScanDeps, repo RepositoryRef) ([]MembershipRef, error) {
	parents, err := deps.Graph.ParentScopes(ctx, scope.Ref{Kind: scope.ScopeKindProject, ID: repo.ID})
	if err != nil {
		return nil, err
	}
	out := make([]MembershipRef, 0, len(parents))
	for _, p := range parents {
		out = append(out, MembershipRef{Kind: string(p.Kind), ID: p.ID})
	}
	return out, nil
}

// hashPath returns a deterministic hex digest of a clean absolute path.
// Never includes the path itself in a stored record beyond this digest --
// the digest is what R-16.3's "path hash" names, not a reversible
// encoding.
func hashPath(absRoot string) string {
	sum := sha256.Sum256([]byte(absRoot))
	return hex.EncodeToString(sum[:])
}

// repositoryID derives a deterministic repository id from remote+path
// hash, so the same tree scanned twice (with no prior scope-graph row)
// still produces the same id both times.
func repositoryID(remote, pathHash string) string {
	sum := sha256.Sum256([]byte(remote + "\x00" + pathHash))
	return hex.EncodeToString(sum[:16])
}
