//go:build !windows

// Purpose: the unix values of the open flags.
//
// Inputs: none.
// Outputs: NoFollow and NonBlock.
// Constraints: values come from syscall for the build platform.
//
// SPORT: pkg/nofollow (ADD) — P1-AGT-01.

package nofollow

import "syscall"

// NoFollow makes os.OpenFile fail when the final path component is a
// symlink; NonBlock makes an open of a FIFO return at once.
const (
	NoFollow int = syscall.O_NOFOLLOW
	NonBlock int = syscall.O_NONBLOCK
)
