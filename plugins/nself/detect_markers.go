// Purpose (this file): the per-directory marker rule of the ancestor scan
//
//	(detect.go) and the home-directory identity check that guards it. Split
//	out of detect.go to keep both files under the line cap; no behaviour of
//	its own beyond what detect.go's scan calls.
//
// Inputs: a statFS and the cleaned home directory, as the scan passes them.
//
// Outputs: whether a directory holds a project marker, and whether a
//
//	directory IS the home directory.
//
// Constraints: no internal/** imports (plugins-providers-boundary depguard).
//
// SPORT: plugins/nself detect (ADD) — P1-PLG-13.

package nself

import (
	"os"
	"path/filepath"
)

// projectMarkerFiles are the files nself itself treats as "this directory is
// a project": a `.nself` directory with one of them BESIDE it (in its parent
// directory) is a project even when the `.nself` directory is still empty,
// which is exactly what `nself init --non-interactive` leaves before any
// build. A bare file without a `.nself` directory never matches, so an
// unrelated repository's `.env` cannot activate the plugin. The never
// committed overlays (.env.secrets, .env.local) are excluded, as nself
// excludes them. A marker must be a file: a `.env` DIRECTORY is a
// virtualenv, not a project file.
//
// PROVENANCE: nself 1.3.5 (commit ff0ba27b), cli
// internal/config/helpers_unknown_vars.go:218 `projectMarkerFiles`, the rule
// FindNSelfRoot walks with; the `init` listing is in testdata/README.md
// § fresh init. nself also checks `<dir>/.backend/` for the monorepo shape;
// that case is out of scope here (detection runs on the directory the
// handshake names).
var projectMarkerFiles = []string{".env", ".env.dev", ".env.staging", ".env.prod"}

// sameFiler is an optional statFS capability: identity comparison of two
// paths (the real filesystem implements it; the fake tree falls back to the
// lexical compare, which is exact for a fake tree).
type sameFiler interface {
	SameFile(a, b string) bool
}

// SameFile reports whether a and b are the same file on disk (os.SameFile
// over two stats), so a symlinked or case-different spelling of one
// directory compares equal. Any stat failure is "not the same".
func (osStatFS) SameFile(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ia, ib)
}

// isHomeDir reports whether dir is the home directory: the same cleaned
// path, or (on a filesystem that can say) the same file, which covers a HOME
// that is a symlink or spelled in a different case. An unresolved (empty)
// home is never matched here; projectMarkerAt fails closed on it instead.
func isHomeDir(fsys statFS, dir, homeDir string) bool {
	if homeDir == "" {
		return false
	}
	if dir == homeDir {
		return true
	}
	if sf, ok := fsys.(sameFiler); ok {
		return sf.SameFile(dir, homeDir)
	}
	return false
}

// projectMarkerAt reports whether dir holds a `.nself` DIRECTORY that
// holds one of projectFiles OR sits beside one of projectMarkerFiles. The
// home directory's own marker is refused outright, before any stat, so no
// content there (including a $HOME/.env) can ever make it match. When the
// home directory is unresolved (empty) the parent-marker branch is skipped
// entirely (fail closed): the home bound is the only thing keeping
// $HOME/.nself + $HOME/.env from reading as a project, and without a home
// there is nothing to bound against. The five-artefact rule still applies.
func projectMarkerAt(fsys statFS, dir, homeDir string) (bool, error) {
	if isHomeDir(fsys, dir, homeDir) {
		return false, nil
	}
	candidate := filepath.Join(dir, markerDirName)
	exists, isDir, err := fsys.Stat(candidate)
	if err != nil || !exists || !isDir {
		return false, err
	}
	var firstSeen error
	for _, name := range projectFiles {
		ok, _, ferr := fsys.Stat(filepath.Join(candidate, name))
		firstSeen = firstErr(firstSeen, ferr)
		if ok {
			return true, firstSeen
		}
	}
	if homeDir == "" {
		return false, firstSeen
	}
	for _, name := range projectMarkerFiles {
		ok, isDir, ferr := fsys.Stat(filepath.Join(dir, name))
		firstSeen = firstErr(firstSeen, ferr)
		if ok && !isDir {
			return true, firstSeen
		}
	}
	return false, firstSeen
}
