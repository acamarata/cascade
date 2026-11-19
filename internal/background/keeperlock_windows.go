//go:build windows

package background

import "os"

// pidAlive reports whether the process with the given pid exists. On Windows
// os.FindProcess opens the process and fails when it is gone.
// It is a variable so tests can override it.
var pidAlive = func(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
