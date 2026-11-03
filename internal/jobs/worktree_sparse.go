package jobs

// Purpose: WorktreeManager.Minimize (R-21.177): shrink a leased worktree
//
//	to its in-scope prefixes plus declared deps with a no-cone sparse
//	checkout, then prove nothing else is checked out.
//
// Inputs: a Worktree row (Path and Repo set by Create) and the job's
//
//	provider.WorktreeScope.
//
// Outputs: nil once the tree holds exactly the scoped paths, or a typed
//
//	error: KindInvalidInput for an unusable scope, KindUnavailable for a
//	git failure, KindIntegrity when the tree still holds an out-of-scope
//	path (or misses an in-scope one) after the sparse checkout.
//
// Constraints: real git over os/exec only (no CGO). The sparse-checkout
//
//	write runs behind the repo's admin mutex because it can enable
//	extensions.worktreeConfig in the shared repository config.
//
// SPORT: jobs/worktree-manager (ADD, P1-E30-W6-S62-T2).

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Minimize applies a `git sparse-checkout set --no-cone` over s's
// InScopePrefixes and DeclaredDeps to w, then verifies that every tracked
// path outside that set is skipped, every tracked path inside it is
// present, and no out-of-scope file remains on disk.
func (m *WorktreeManager) Minimize(ctx context.Context, w Worktree, s provider.WorktreeScope) error {
	if w.Path == "" {
		return cascade.New(cascade.KindInvalidInput, "jobs: minimize needs a worktree path")
	}
	scope, err := normalizeSparseScope(s)
	if err != nil {
		return err
	}
	repo := w.Repo
	if repo == "" {
		repo = w.Path
	}
	args := append([]string{"sparse-checkout", "set", "--no-cone"}, sparsePatterns(scope)...)
	if _, err := m.runGitAdmin(ctx, repo, w.Path, args...); err != nil {
		return err
	}
	if err := m.verifySparseIndex(ctx, w.Path, scope); err != nil {
		return err
	}
	return verifySparseDisk(w.Path, scope)
}

// normalizeSparseScope cleans every prefix and dep to a slash-separated,
// repo-relative path, refusing an empty set, absolute paths, traversal
// out of the tree and the repository's own .git entry.
func normalizeSparseScope(s provider.WorktreeScope) ([]string, error) {
	raw := append(append([]string{}, s.InScopePrefixes...), s.DeclaredDeps...)
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		slashed := filepath.ToSlash(p)
		if strings.TrimSpace(slashed) == "" || path.IsAbs(slashed) || filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
			return nil, cascade.Newf(cascade.KindInvalidInput, "jobs: minimize scope path %q is not repo-relative", p)
		}
		clean := path.Clean(slashed)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, cascade.Newf(cascade.KindInvalidInput, "jobs: minimize scope path %q escapes the worktree", p)
		}
		if clean == ".git" || strings.HasPrefix(clean, ".git/") {
			return nil, cascade.Newf(cascade.KindInvalidInput, "jobs: minimize scope path %q names git metadata", p)
		}
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	if len(out) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "jobs: minimize needs at least one in-scope prefix or dep")
	}
	return out, nil
}

// sparsePatterns renders each scoped path as an anchored no-cone pattern
// with gitignore metacharacters escaped, so a prefix matches only itself
// and everything beneath it, never a same-named path deeper in the tree.
func sparsePatterns(scope []string) []string {
	out := make([]string, 0, len(scope))
	for _, p := range scope {
		var b strings.Builder
		b.WriteByte('/')
		for _, r := range p {
			if strings.ContainsRune(`\*?[]!# `, r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		out = append(out, b.String())
	}
	return out
}

// inSparseScope reports whether the slash-separated repo-relative p is a
// scoped path or lies beneath one.
func inSparseScope(p string, scope []string) bool {
	for _, s := range scope {
		if p == s || strings.HasPrefix(p, s+"/") {
			return true
		}
	}
	return false
}

// leadsToSparseScope reports whether directory p is an ancestor of a
// scoped path (a directory git must create to hold it).
func leadsToSparseScope(p string, scope []string) bool {
	for _, s := range scope {
		if strings.HasPrefix(s, p+"/") {
			return true
		}
	}
	return false
}

// verifySparseIndex reads `git ls-files -t -z` and refuses any tracked
// path whose skip-worktree state disagrees with the scope: an out-of-scope
// path still checked out, or an in-scope path skipped.
func (m *WorktreeManager) verifySparseIndex(ctx context.Context, dir string, scope []string) error {
	out, err := m.git.run(ctx, dir, "ls-files", "-t", "-z")
	if err != nil {
		return err
	}
	for _, rec := range strings.Split(out, "\x00") {
		if len(rec) < 3 || rec[1] != ' ' {
			continue
		}
		tag, p := rec[0], rec[2:]
		skipped := tag == 'S'
		in := inSparseScope(p, scope)
		if !in && !skipped {
			return cascade.Newf(cascade.KindIntegrity, "jobs: minimize left out-of-scope path %q checked out in %s", p, dir)
		}
		if in && skipped {
			return cascade.Newf(cascade.KindIntegrity, "jobs: minimize skipped in-scope path %q in %s", p, dir)
		}
	}
	return nil
}

// verifySparseDisk walks the worktree and refuses any file, symlink or
// directory outside the scope (bar the root .git entry and the parent
// directories of scoped paths), so untracked leftovers fail too.
func verifySparseDisk(root string, scope []string) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "jobs: minimize could not walk %s", p)
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return cascade.Wrapf(cascade.KindInternal, err, "jobs: minimize could not relativize %s", p)
		}
		rel = filepath.ToSlash(rel)
		if rel == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if inSparseScope(rel, scope) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() && leadsToSparseScope(rel, scope) {
			return nil
		}
		return cascade.Newf(cascade.KindIntegrity, "jobs: minimize left out-of-scope path %q on disk in %s", rel, root)
	})
}
