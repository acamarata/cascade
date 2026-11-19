//go:build !windows

package background

import "syscall"

// pidAlive reports whether the process with the given pid is alive.
// It is a variable so tests can override it.
var pidAlive = func(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
