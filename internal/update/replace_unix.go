//go:build !windows

package update

// Purpose: replace dst with src by one rename (atomic on unix, even while
//   the old executable runs), and restore a swapped file the same way.
// SPORT: internal/update replaceFile/ADDED.

func replaceFile(src, dst string) error { return renameFile(src, dst) }

// restoreFile renames a staged copy of the previous file over target.
func restoreFile(target, prev string) error { return restoreCopy(target, prev) }
