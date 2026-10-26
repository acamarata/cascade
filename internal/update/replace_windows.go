//go:build windows

package update

// Purpose: replace dst with src on windows, where a running executable
//   cannot be overwritten but can be renamed: rename-old, then move the
//   new file into place. The running process keeps <name>.old open; the
//   next start runs the new file. Restore undoes it by renames only.
// Constraints: a stale <name>.old is removed first; if it is still in use
//   that removal fails and the rename below refuses, leaving dst unchanged.
//   restoreFile never deletes or overwrites <name>.old (it may be the
//   running image): it moves the new file aside to <name>.failed-<n> and
//   renames <name>.old back. Without a <name>.old it falls back to the
//   previous copy (restoreCopy).
// SPORT: internal/update replaceFile/ADDED.

import (
	"errors"
	"os"
	"strconv"
)

// maxAsideNames bounds the search for a free <name>.failed-<n>.
const maxAsideNames = 100

func replaceFile(src, dst string) error {
	old := dst + ".old"
	_ = os.Remove(old)
	if _, err := os.Lstat(dst); err == nil {
		if err := renameFile(dst, old); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := renameFile(src, dst); err != nil {
		_ = renameFile(old, dst)
		return err
	}
	return nil
}

// restoreFile moves the new target aside and renames <target>.old back.
func restoreFile(target, prev string) error {
	old := target + ".old"
	if _, err := os.Lstat(old); err != nil {
		return restoreCopy(target, prev)
	}
	aside, err := asideName(target)
	if err != nil {
		return err
	}
	if err := renameFile(target, aside); err != nil {
		return unavailable(err, "move %s aside to restore it", target)
	}
	if err := renameFile(old, target); err != nil {
		_ = renameFile(aside, target)
		return unavailable(err, "rename %s back to %s", old, target)
	}
	return nil
}

// asideName is the first free <target>.failed-<n>.
func asideName(target string) (string, error) {
	for n := 1; n <= maxAsideNames; n++ {
		p := target + ".failed-" + strconv.Itoa(n)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
	}
	return "", unavailable(os.ErrExist, "find a free name to move %s aside", target)
}
