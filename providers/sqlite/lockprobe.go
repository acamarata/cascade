package sqlite

import (
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: ProbeExclusiveLock — a non-blocking, non-acquiring check of
//
//	whether the §D-3 sidecar exclusive lock on a database path is
//	currently held, exported for internal/storage/health.go's
//	StorageHealthCheck flock-probe check (P1-E02-W1-S03-T1). Split from
//	driver.go under R-14.117 (Art.10.3's 300-line file cap): adding this
//	function pushed driver.go to 351 lines, and the split is mechanical
//	— moved code only, no behaviour change, same package.
//
// Inputs: a filesystem path to the .db file being probed (the same path
//
//	Open would take).
//
// Outputs: a LockProbeResult describing whether the lock is held, or the
//
//	windows tier-2 refusal; a non-nil error only for a genuinely
//	unexpected failure (e.g. the sidecar lock file's directory is
//	unreadable) neither of the two expected outcomes covers.
//
// Constraints: providers/** may import pkg/** only, never internal/**
//
//	(Art.10.2) — this is why the function lives here (internal/storage
//	imports providers/sqlite, never the reverse) rather than the other
//	way around.
//
// SPORT: providers.sqlite.driver/CHANGED (P1-E02-W1-S03-T1).

// LockProbeResult reports the outcome of ProbeExclusiveLock's non-blocking,
// non-acquiring check for the §D-3 sidecar exclusive lock. Exactly one of
// Held or Unsupported is meaningful per platform: darwin/linux report Held
// (Unsupported always false); windows always reports Unsupported (Held is
// meaningless there, since flock_windows.go's acquireExclusiveLock never
// actually probes anything — see ProbeExclusiveLock's doc comment).
type LockProbeResult struct {
	// Held reports that another process currently holds the exclusive
	// lock on the probed database file. ProbeExclusiveLock cannot tell a
	// live owner apart from a stale lock left by a crashed process — both
	// present identically at the flock(2) layer — so callers
	// (StorageHealthCheck) report presence only, never "stale" as a firm
	// diagnosis.
	Held bool
	// Unsupported reports the windows tier-2 refusal: this platform has
	// no real probe implementation, so Held carries no information.
	Unsupported bool
	// Detail is a human-readable elaboration (the underlying refusal or
	// conflict message), always populated when Held or Unsupported is
	// true.
	Detail string
}

// ProbeExclusiveLock performs a non-blocking, non-acquiring check of
// whether the §D-3 exclusive lock on path is currently held, for
// StorageHealthCheck's stale-lock-presence check (internal/storage/
// health.go, which may import this package but never the reverse —
// Art.10.2 only denies providers/** importing internal/**). It wraps the
// SAME per-platform acquireExclusiveLock helpers Open already uses
// (flock_darwin.go / flock_linux.go's LOCK_EX|LOCK_NB, flock_windows.go's
// tier-2 refusal): on darwin/linux it attempts the identical non-blocking
// acquire and, if it succeeds, releases immediately before returning — this
// function never holds the lock beyond the probe itself, so it can never be
// the thing that makes a real Open elsewhere fail. A cascade.KindConflict
// failure means some other lock holder (live daemon or a stale lock from a
// crashed process) currently owns it; a cascade.KindUnsupported failure
// (windows) is reported via Unsupported, not returned as an error — a
// health check must not fail merely because it is running on a platform
// this ticket's flock support does not yet cover. Any other error (e.g.
// the sidecar lock file's directory is unreadable) is returned as-is for
// the caller to wrap with its own taxonomy context.
func ProbeExclusiveLock(path string) (LockProbeResult, error) {
	unlock, err := acquireExclusiveLock(path)
	if err == nil {
		// Nobody else holds it — release immediately; this is a probe,
		// never a real acquire.
		return LockProbeResult{Held: false}, unlock()
	}

	var cerr *cascade.Error
	if !errors.As(err, &cerr) {
		return LockProbeResult{}, err
	}
	// An if/else chain, not a switch: golangci-lint's `exhaustive`
	// analyzer (R-14.101) requires a SWITCH over cascade.Kind to name
	// every one of the 14 taxonomy members, which would falsely imply
	// this function has a real branch for taxonomy kinds
	// acquireExclusiveLock's two platform implementations never
	// produce — they return only KindUnavailable (I/O setup failure,
	// handled above via the errors.As check), KindConflict, or
	// KindUnsupported.
	if cerr.Kind == cascade.KindConflict {
		return LockProbeResult{Held: true, Detail: cerr.Error()}, nil
	}
	if cerr.Kind == cascade.KindUnsupported {
		return LockProbeResult{Unsupported: true, Detail: cerr.Error()}, nil
	}
	return LockProbeResult{}, err
}
