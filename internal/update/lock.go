package update

// Purpose: the inter-process lock one Apply holds from planning through the
//   receipt rewrite, so two self-updates never interleave (a paused updater
//   with a stale plan could otherwise install an older release over the one
//   another updater just installed).
// Inputs: <dataDir>.
// Outputs: a held lock on <dataDir>/update/lock, or KindConflict naming
//   "another self-update is running".
// Constraints: non-blocking: a held lock refuses at once and changes
//   nothing. The lock file, and every directory created to hold it, is
//   removed on release, so a refused or no-op Apply leaves the tree as it
//   found it. A lock taken on a file that is no longer at the path (a
//   releasing holder removed it) is dropped and retried. Check never takes
//   the lock. Platform halves: lock_unix.go (flock), lock_windows.go
//   (LockFileEx).
// SPORT: internal/update acquireUpdateLock/ADDED.

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// lockAttempts bounds the retries after a lock on a replaced file.
const lockAttempts = 3

// updateLock is one held self-update lock.
type updateLock struct {
	f    *os.File
	path string
	made []string // directories created for the lock, deepest first
}

// lockPath is where Apply's lock lives under dataDir.
func lockPath(dataDir string) string { return filepath.Join(dataDir, "update", "lock") }

// acquireUpdateLock takes the self-update lock without blocking.
func acquireUpdateLock(dataDir string) (*updateLock, error) {
	l := &updateLock{path: lockPath(dataDir)}
	made, err := makeDirs(filepath.Dir(l.path))
	if err != nil {
		return nil, err
	}
	l.made = made
	for i := 0; i < lockAttempts; i++ {
		f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			l.removeDirs()
			return nil, unavailable(err, "open the self-update lock %s", l.path)
		}
		ok, err := tryLockFile(f)
		if err != nil || !ok {
			_ = f.Close()
			l.removeDirs()
			if err != nil {
				return nil, unavailable(err, "take the self-update lock %s", l.path)
			}
			return nil, cascade.Newf(cascade.KindConflict,
				"update: another self-update is running (%s is held); nothing was changed", l.path)
		}
		if lockedFileIsPath(f, l.path) {
			l.f = f
			return l, nil
		}
		unlockFile(f)
		_ = f.Close()
	}
	l.removeDirs()
	return nil, cascade.Newf(cascade.KindUnavailable, "update: the self-update lock %s kept changing; retry", l.path)
}

// release drops the lock and removes the lock file and created directories.
func (l *updateLock) release() {
	releaseLockFile(l.f, l.path)
	l.removeDirs()
}

// removeDirs removes the directories made for the lock while they are empty.
func (l *updateLock) removeDirs() {
	for _, d := range l.made {
		_ = os.Remove(d)
	}
}

// lockedFileIsPath reports whether f is still the regular file at path.
func lockedFileIsPath(f *os.File, path string) bool {
	held, err := f.Stat()
	if err != nil {
		return false
	}
	cur, err := os.Lstat(path)
	return err == nil && cur.Mode().IsRegular() && os.SameFile(held, cur)
}

// makeDirs creates dir and lists the directories it had to create,
// deepest first.
func makeDirs(dir string) ([]string, error) {
	var made []string
	for d := filepath.Clean(dir); filepath.Dir(d) != d; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); !errors.Is(err, os.ErrNotExist) {
			break
		}
		made = append(made, d)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, unavailable(err, "create %s", dir)
	}
	return made, nil
}
