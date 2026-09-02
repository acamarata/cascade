// Purpose: canonicalizes a caller-supplied database path to the resolved,
//   symlink-free absolute form the §D-3 sidecar lock file's name is
//   derived from, so two different spellings of the same database file —
//   a relative path vs an absolute one, or a path vs a symlink pointing at
//   it — always collapse to the same "<resolved>.lock" file and therefore
//   contend on the same OS-level flock. Without this, flock_darwin.go and
//   flock_linux.go each derived the lock path from path+".lock" verbatim,
//   so two processes opening the same database through different
//   spellings would each take an unrelated flock and never see each
//   other — defeating the "never two writers" invariant this ticket
//   exists to enforce.
// Inputs: path, exactly the string Open's caller passed (may be relative,
//   may traverse a symlink, may not exist yet).
// Outputs: the fully resolved absolute path, or a *cascade.Error if
//   neither path nor its parent directory can be resolved at all.
// Constraints: shared by flock_darwin.go and flock_linux.go, both of which
//   call this before appending the ".lock" suffix; flock_windows.go never
//   acquires a lock, so it has no need of this file. Must handle the
//   normal first-open case where the database file does not exist yet —
//   filepath.EvalSymlinks fails on a missing leaf component.
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T2 CR fix).

package sqlite

import (
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// canonicalDBPath resolves path to an absolute, symlink-free form so the
// §D-3 sidecar lock file's name is stable across equivalent spellings of
// the same database (relative vs absolute, or a path vs a symlink
// pointing at it).
func canonicalDBPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: resolve absolute path for %s", path)
	}

	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "sqlite: resolve symlinks for %s", abs)
	}

	// The database file does not exist yet — the normal first-open case.
	// EvalSymlinks cannot resolve a missing leaf, so resolve the parent
	// directory instead and rejoin the base name. The base name itself is
	// not a symlink by construction here (abs's leaf component failed to
	// resolve, meaning it doesn't exist as any kind of filesystem entry
	// yet), so resolving only the directory component is sufficient to
	// make relative-vs-absolute and symlinked-parent-directory spellings
	// collapse to the same canonical path.
	dir := filepath.Dir(abs)
	resolvedDir, dirErr := filepath.EvalSymlinks(dir)
	if dirErr != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, dirErr, "sqlite: resolve parent directory for %s", abs)
	}
	return filepath.Join(resolvedDir, filepath.Base(abs)), nil
}
