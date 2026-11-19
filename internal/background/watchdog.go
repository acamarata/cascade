// Package background provides watchdog supervision for capp gc crunches.
package background

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// ErrUnrecorded is returned when a crunch is not recorded in the spec.
var ErrUnrecorded = errors.New("D1479: unrecorded crunch")

// WatchdogSpec describes the recorded crunch that the watchdog may execute.
type WatchdogSpec struct {
	// Crunch is the only permitted capp gc crunch command.
	Crunch string
}

// RunWatchdog executes the given crunch only if it matches spec.Crunch and no STOP marker exists.
// It returns "executed" on success, or "halted" if a STOP marker is present.
func RunWatchdog(ctx context.Context, spec WatchdogSpec, crunch, stopDir string, exec func(context.Context, string) error) (string, error) {
	if crunch != spec.Crunch {
		return "", ErrUnrecorded
	}

	stopPath := filepath.Join(stopDir, "STOP")
	if _, err := os.Stat(stopPath); err == nil {
		return "halted", nil
	}

	if err := exec(ctx, crunch); err != nil {
		return "", err
	}

	return "executed", nil
}
