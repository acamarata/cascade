//go:build unix

package context

import (
	"errors"
	"os"
	"syscall"
)

// Purpose: open a tier file without following a final symlink on unix, so a
//   tier file swapped for a link between the Lstat and the open is refused by
//   the kernel (ELOOP) instead of read, and a FIFO never blocks the open.
// Inputs: a path. Outputs: the opened file. Constraints: read-only.
// SPORT: context-engine/discovery.

// openNoFollow opens path read-only with O_NOFOLLOW and O_NONBLOCK, so a
// FIFO swapped in after the Lstat cannot block the open; the caller's fstat
// then refuses anything that is not a regular file.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// isSymlinkLoop reports the ELOOP that O_NOFOLLOW returns for a symlink.
func isSymlinkLoop(err error) bool { return errors.Is(err, syscall.ELOOP) }
