// Purpose (this file): the D1 fail-closed changed-path -> owning-package
// resolution affected_go.go's affectedGoTargets depends on, split out of
// that file to stay under Art.10.3's 300-line file cap.
//
// Inputs: worktreeRoot (a real Go module checkout) and a changed-path
// list (repository-relative, as ChangedPaths returns them).
// Outputs: the set of real import paths each changed path resolves to,
// plus ok=false the instant any changed path cannot be resolved at all.
// Constraints: fail-closed mapping (06 §5.20, REWORK CR round): every
// changed path must map to at least one target or force the caller to
// TargetAll -- go.mod/go.sum/go.work(.sum), vendor/, a deleted-package
// directory, an unresolvable module-root file, and any other directory
// `go list` refuses all report ok=false, never "contributes nothing". A
// path under a package's testdata/ (at any depth) instead walks up to
// the nearest enclosing directory `go list` DOES resolve, since
// testdata/ is never itself a buildable package by Go's own convention.
// SPORT: internal.ci.changedOwningPackages/ADDED (P1-E32-W6-S65-T1, fixed).

package ci

import (
	"context"
	"path"
	"strings"
)

// changedOwningPackages resolves every changed path to its owning
// package's real import path via resolveOwningDir, deduplicated by
// directory. ok is false the instant ANY changed path cannot be
// resolved to a real target -- a go.mod/go.sum/go.work(.sum) change, a
// vendor/ change, or a directory `go list` refuses even after the
// testdata/ walk-up (a deleted package, a module-root file with no root
// package, or any other unresolvable path) -- the caller then forces
// TargetAll rather than silently dropping that path: the ONLY empty
// selection this function's caller may ever read as legitimate is an
// empty `changed` slice, handled before this function is even called.
func changedOwningPackages(ctx context.Context, worktreeRoot string, changed []string) (map[string]bool, bool) {
	dirs := make(map[string]bool, len(changed))
	for _, p := range changed {
		if changedPathForcesFull(p) {
			return nil, false
		}
		dirs[path.Dir(path.Clean(p))] = true
	}
	owning := make(map[string]bool, len(dirs))
	for dir := range dirs {
		pkg, ok := resolveOwningDir(ctx, worktreeRoot, dir)
		if !ok {
			return nil, false
		}
		owning[pkg] = true
	}
	return owning, true
}

// changedPathForcesFull reports whether p's own path -- independent of
// whatever directory go list could or could not resolve it to -- always
// forces TargetAll: go.mod, go.sum, go.work, go.work.sum, and anything
// under vendor/. These change the module's own dependency/vendoring
// surface, which the direct-import graph affected_go.go builds cannot
// express edges for at all; a literal-filename check runs before any
// subprocess, so these never cost a `go list` call.
func changedPathForcesFull(p string) bool {
	cleaned := path.Clean(p)
	switch path.Base(cleaned) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	return cleaned == "vendor" || strings.HasPrefix(cleaned, "vendor/")
}

// resolveOwningDir resolves dir to its real owning package import path.
// It tries dir itself first; if that fails and dir is (or is nested
// under) a directory literally named "testdata" -- go's own convention
// for data files a package's tests read, never itself buildable -- it
// walks up one directory at a time, retrying at each level, until an
// ancestor resolves or the walk leaves testdata/ entirely. A non-testdata
// resolution failure (a deleted package's directory, an unresolvable
// module-root file) is NOT walked up: only a real testdata/ nesting gets
// the "nearest enclosing package" treatment -- this function's own
// worked distinction between "walk up" and "force full".
func resolveOwningDir(ctx context.Context, worktreeRoot, dir string) (string, bool) {
	if pkg, ok := goListDirImportPath(ctx, worktreeRoot, dir); ok {
		return pkg, true
	}
	for underTestdata(dir) {
		dir = path.Dir(dir)
		if pkg, ok := goListDirImportPath(ctx, worktreeRoot, dir); ok {
			return pkg, true
		}
	}
	return "", false
}

// underTestdata reports whether dir is, or is nested under, a directory
// literally named "testdata".
func underTestdata(dir string) bool {
	for d := dir; d != "." && d != "/" && d != ""; d = path.Dir(d) {
		if path.Base(d) == "testdata" {
			return true
		}
	}
	return false
}
