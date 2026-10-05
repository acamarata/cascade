package scope

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: CanonicalRepoRoot is the repository-identity-of-record
//   resolver. It turns any path inside a repository into one canonical,
//   symlink-resolved root, so a linked worktree, a snapshot checkout and
//   a symlinked path of one repository all collapse to one value -- the
//   identity EnsureRepository (ensure_repository.go) keys
//   context_repository rows on -- while two submodules of one
//   super-repository stay two identities.
// Inputs: an absolute path p and an injected GitCommonDirFunc. This
//   package imports no os/exec (TestScopeImportsNoExec enforces it): the
//   A-T2 exec allowlist (internal/build/egress_allow.go) admits
//   internal/context, not internal/context/scope, so the one production
//   implementation, internal/context.GitCommonDir, lives there and is
//   injected here -- the same seam shape as resolver.go's GitRootFunc.
// Outputs: the canonical, symlink-resolved, cleaned root path, or a typed
//   cascade.Error: KindInvalidInput for a nil git func, a non-absolute
//   p, a p git reports is not inside a repository, or a layout with no
//   deterministic root (repo_root_ambiguous); whatever Kind git's own
//   error already carries otherwise (KindUnavailable when git could not
//   run at all).
// Constraints: deterministic and fail-closed; `--show-toplevel` is used
//   only for the main worktree (step 3 below), never as the sole rule.
// SPORT: context/repo-identity/ADD.

// GitDirs is what a GitCommonDirFunc reports about one path. Paths are
// absolute. Bare is core.bare from <CommonDir>/config, so it is true in a
// bare repository and in every linked worktree of one. CoreWorktree is
// the raw core.worktree value from the same file ("" when unset);
// Toplevel is "" when Bare and for a path git reports is not in a work
// tree.
type GitDirs struct {
	CommonDir, GitDir, CoreWorktree, Toplevel string
	Bare                                      bool
}

// GitCommonDirFunc resolves the GitDirs of dir. This package declares
// only the function TYPE: internal/context.GitCommonDir is the one
// production implementation, injected by every caller (the exec
// allowlist admits internal/context, not this package).
type GitCommonDirFunc func(ctx context.Context, dir string) (GitDirs, error)

// errRepoRootAmbiguous names the refusal for a layout with no
// deterministic root; callers see it in the KindInvalidInput message.
const errRepoRootAmbiguous = "repo_root_ambiguous"

// CanonicalRepoRoot resolves p to the canonical repository root, then
// collapses any symlinked ancestor (e.g. a `~/Sites` ->
// `/mnt/data/Sites` mount) with filepath.EvalSymlinks and cleans it.
func CanonicalRepoRoot(ctx context.Context, p string, git GitCommonDirFunc) (string, error) {
	if git == nil {
		return "", cascade.New(cascade.KindInvalidInput, "context/scope: CanonicalRepoRoot requires a non-nil git")
	}
	if !filepath.IsAbs(p) {
		return "", cascade.Newf(cascade.KindInvalidInput, "context/scope: CanonicalRepoRoot requires an absolute path, got %q", p)
	}

	d, err := git(ctx, p)
	if err != nil {
		// git's implementation already carries the right Kind; keep it.
		return "", err
	}
	root, err := chooseRepoRoot(p, d)
	if err != nil {
		return "", err
	}

	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "context/scope: resolve symlinks for %q", root)
	}
	return filepath.Clean(resolved), nil
}

// chooseRepoRoot applies the fixed order: (0) a bare common dir is the
// root, for the bare dir itself and for every worktree of it; otherwise (1) a common dir named `.git` means its parent, (2) a
// set core.worktree (made absolute against the common dir) names the
// root, (3) the main worktree (GitDir == CommonDir) uses its toplevel,
// and (4) anything else refuses rather than guess.
func chooseRepoRoot(p string, d GitDirs) (string, error) {
	switch {
	case d.Bare:
		return d.CommonDir, nil
	case filepath.Base(d.CommonDir) == ".git":
		return filepath.Dir(d.CommonDir), nil
	case d.CoreWorktree != "":
		if filepath.IsAbs(d.CoreWorktree) {
			return filepath.Clean(d.CoreWorktree), nil
		}
		return filepath.Join(d.CommonDir, d.CoreWorktree), nil
	case d.GitDir == d.CommonDir && d.Toplevel != "":
		return d.Toplevel, nil
	}
	return "", cascade.Newf(cascade.KindInvalidInput,
		"context/scope: %s: no deterministic repository root for %q (common dir %q, git dir %q)",
		errRepoRootAmbiguous, p, d.CommonDir, d.GitDir)
}
