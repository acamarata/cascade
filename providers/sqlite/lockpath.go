// Purpose: canonicalizes a caller-supplied database path to the resolved,
//   symlink-free absolute form the §D-3 sidecar lock file's name is
//   derived from, so two different spellings of the same database file —
//   a relative path vs an absolute one, a path vs a symlink pointing at
//   it, or (on a case-insensitive filesystem) two different spellings of
//   the same basename — always collapse to the same "<resolved>.lock"
//   file and therefore contend on the same OS-level lock. Without this,
//   flock_darwin.go, flock_linux.go and flock_windows.go each derived the
//   lock path from path+".lock" verbatim, so two processes opening the
//   same database through different spellings would each take an
//   unrelated lock and never see each other — defeating the "never two
//   writers" invariant this ticket exists to enforce.
// Inputs: path, exactly the string Open's caller passed (may be relative,
//   may traverse a symlink, may not exist yet).
// Outputs: the fully resolved absolute path, or a *cascade.Error if
//   neither path nor its parent directory can be resolved at all.
// Constraints: shared by flock_darwin.go, flock_linux.go and
//   flock_windows.go, all of which call this before appending the ".lock"
//   suffix. Must handle the normal first-open case where the database
//   file does not exist yet — filepath.EvalSymlinks fails on a missing
//   leaf component — and on that path must also normalize case when the
//   parent directory turns out to be case-insensitive (NTFS's and APFS's
//   default): "cascade.db" and "Cascade.DB" are the SAME on-disk file
//   there, so a verbatim rejoin of two different-case spellings would
//   silently derive two DIFFERENT sidecar lock names and let both callers
//   "win" the lock. The already-exists branch needs no equivalent fix:
//   filepath.EvalSymlinks resolves through the real filesystem entry, so
//   it already returns the on-disk canonical spelling on both platforms.
// SPORT: providers.sqlite.Driver/CHANGED (P1-E02-W1-S02-T2 CR fix;
//   case-insensitive-basename fix, windows LockFileEx ticket).

package sqlite

import (
	"os"
	"path/filepath"
	"strings"

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

	base := filepath.Base(abs)
	if dirIsCaseInsensitive(resolvedDir) {
		// See this file's package doc: on a case-insensitive filesystem two
		// different-case spellings of the same not-yet-existing basename
		// are the SAME on-disk file, so normalize to one spelling before
		// the caller appends ".lock" — otherwise they'd derive two
		// different sidecar names and never contend.
		base = strings.ToLower(base)
	}
	return filepath.Join(resolvedDir, base), nil
}

// caseProbePrefix names the temp file dirIsCaseInsensitive creates to test
// resolvedDir's case sensitivity. It is deliberately all-uppercase letters
// (os.CreateTemp's own random suffix is digits only) so
// strings.ToLower(name) always differs from name — the property the probe
// depends on to mean anything.
const caseProbePrefix = "CASCADE-LOCKPATH-CASEPROBE-*.tmp"

// dirIsCaseInsensitive reports whether dir's filesystem treats two
// different-case spellings of the same name as the same file, by creating
// a real probe file and Stat-ing its lower-cased name. It errs toward
// false (case-sensitive, i.e. "do not normalize") on any failure to
// determine the answer, which preserves this function's pre-fix verbatim
// behavior rather than risking merging two genuinely distinct files.
func dirIsCaseInsensitive(dir string) bool {
	f, err := os.CreateTemp(dir, caseProbePrefix)
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	defer func() { _ = os.Remove(name) }()

	lower := strings.ToLower(filepath.Base(name))
	upper := filepath.Base(name)
	if lower == upper {
		// caseProbePrefix guarantees this cannot happen; defensive only.
		return false
	}

	info, err := os.Stat(name)
	if err != nil {
		return false
	}
	altInfo, err := os.Stat(filepath.Join(dir, lower))
	if err != nil {
		return false
	}
	return os.SameFile(info, altInfo)
}
