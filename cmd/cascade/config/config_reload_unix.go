//go:build !windows

package config

// Purpose: the real SIGHUP-send behind `cascade config reload` on every
//   non-Windows platform.

import (
	"os"
	"syscall"

	"github.com/acamarata/cascade/pkg/cascade"
)

// sendReloadSignalToPID sends SIGHUP to pid.
func sendReloadSignalToPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "find daemon process")
	}
	if err := proc.Signal(syscall.SIGHUP); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "send SIGHUP to daemon")
	}
	return nil
}
