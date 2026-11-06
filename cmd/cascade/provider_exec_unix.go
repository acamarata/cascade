// Purpose: unix-specific exec-in-place for `cascade provider exec`.
// Inputs: resolved command path, args, scrubbed environment.
// Outputs: never returns on success; error on failure.
// Constraints: syscall.Exec replaces the process; exit status is the command's.
// SPORT: cli.provider.exec/ADD (P1-GWY-10a, contract:gateway-operator-exec).

//go:build !windows

package main

import (
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// platformExec replaces the current process with the command using syscall.Exec.
// It never returns on success; on failure it returns an error.
func platformExec(path string, args []string, env []string) error {
	// syscall.Exec replaces the process image. If it returns, it failed.
	err := syscall.Exec(path, args, env)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "provider exec: syscall.Exec failed")
	}
	// Unreachable, but the compiler needs this
	return nil
}
