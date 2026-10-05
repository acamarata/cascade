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
	// GitCommonDir is required: it routes repository identity through
	// scope.GraphStore.EnsureRepository, the one writer of
	// context_repository rows. production passes
	// internal/context.GitCommonDir.
	GitCommonDir scope.GitCommonDirFunc
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
	case deps.GitCommonDir == nil:
		return cascade.New(cascade.KindInvalidInput, "repo: Scan requires a non-nil GitCommonDir")
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

// resolveRepository binds absRoot to its repository row through
// scope.GraphStore.EnsureRepository, which resolves the CanonicalRepoRoot
// first so a linked worktree or a symlinked path of
// one repository binds to one id. EnsureRepository is the only writer of
// context_repository rows; this package derives no id itself.
func resolveRepository(ctx context.Context, deps ScanDeps, absRoot string) (RepositoryRef, error) {
	remote := deps.RemoteURL(ctx, absRoot)
	rec, err := deps.Graph.EnsureRepository(ctx, absRoot, remote, deps.GitCommonDir)
	if err != nil {
		return RepositoryRef{}, err
	}
	return RepositoryRef{ID: rec.ID, Remote: rec.Remote, PathHash: rec.PathHash}, nil
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
