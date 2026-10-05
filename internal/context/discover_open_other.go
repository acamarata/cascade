//go:build !unix

package context

import "os"

// Purpose: open a tier file where O_NOFOLLOW does not exist (windows). The
//   caller's Lstat + os.SameFile check still refuses a swapped-in link.
// Inputs: a path. Outputs: the opened file. Constraints: read-only.
// SPORT: context-engine/discovery.

// openNoFollow opens path read-only.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

// isSymlinkLoop is always false where no ELOOP exists.
func isSymlinkLoop(error) bool { return false }
