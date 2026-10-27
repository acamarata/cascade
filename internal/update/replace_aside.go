package update

// Purpose: the windows replace sequence (rename-old, then move the new file
//   into place) as a platform-neutral helper that takes the rename as a
//   parameter, so the failure paths are testable on every host.
// Constraints: a stale <name>.old is removed first. If moving src into place
//   fails, <name>.old is renamed back; if that also fails dst is gone, and
//   the error says so and names install.sh (Swap restores only the files it
//   swapped, which excludes this one).
// SPORT: internal/update replaceFile/ADDED.

import (
	"errors"
	"os"
)

// replaceByRenameAside moves src over dst by setting dst aside as dst+".old"
// first. It returns nil on success. When moving src into place fails it puts
// dst+".old" back, and returns the move error; if the put-back also fails
// it returns both errors joined with the install.sh recovery hint.
func replaceByRenameAside(rename func(src, dst string) error, src, dst string) error {
	old := dst + ".old"
	_ = os.Remove(old)
	if _, err := os.Lstat(dst); err == nil {
		if err := rename(dst, old); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rename(src, dst); err != nil {
		if _, serr := os.Lstat(old); serr != nil {
			return err // nothing was set aside (dst did not exist): nothing to put back
		}
		if rerr := rename(old, dst); rerr != nil {
			return errors.Join(err, restoreFailed(unavailable(rerr, "rename %s back to %s", old, dst)))
		}
		return err
	}
	return nil
}
