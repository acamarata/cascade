// Purpose: recovery.go's Scan step implementations — probeSocket (HOW
//   step 1), scanPidfile (step 2/3), scanOrphanedLocks (step 4), and
//   publishRecoveryEvent (step 5). Split from recovery.go under R-14.117
//   (Art.10.3's 300-line cap); moved code only, same package, same
//   ticket.
// SPORT: runtime/recovery (ADD).

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"
)

// probeSocket attempts a connection to the daemon socket at path via
// dial. Returns (live=true, ...) when a listener answers — the caller
// aborts immediately, no cleanup performed. Returns (false, stale=true,
// nil) ONLY for the two confirmed-stale cases the contract names: the
// socket file exists but nothing answers (ECONNREFUSED). A missing
// socket file is reported as stale=false (nothing to clean — the
// ordinary clean-startup case). Any other dial failure (permission
// denied, an unexpected network error) is UNDECIDABLE and returned as an
// error — conservative per the brief: when in doubt, do not proceed to
// remove anything.
func probeSocket(path string, timeout time.Duration, dial Dialer) (live, stale bool, err error) {
	// os.Lstat, not os.Stat (R-14.161, decided at the W1 hardening gate).
	// Stat FOLLOWS symlinks, so a dangling symlink at path (its target
	// removed, the link itself left behind) resolved to ENOENT exactly
	// like a genuinely missing socket file and was classified "clean
	// state". The later net.Listen(path) then failed against a path that
	// still holds a directory entry, and the operator saw a startup
	// failure the recovery scan claimed to have already handled.
	//
	// Lstat sees the link itself, so a dangling link is no longer read as
	// "nothing here": the probe proceeds to dial, the dial fails with
	// ENOENT rather than ECONNREFUSED, and the scan returns the
	// undecidable error below. That is deliberate. The scanner still
	// removes nothing, because a symlink at the socket path could be an
	// operator's redirect rather than scanner debris, and deciding to
	// delete it is a behavior change this gate did not authorize. What
	// changes is only that the state is reported loudly instead of
	// silently mislabelled clean.
	if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
		// No socket file at all: nothing was left behind, nothing to
		// probe. This is the ordinary clean-startup case.
		return false, false, nil
	}

	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	conn, dialErr := dial("unix", path, timeout)
	if dialErr == nil {
		_ = conn.Close()
		return true, false, nil
	}
	if errors.Is(dialErr, syscall.ECONNREFUSED) {
		return false, true, nil
	}
	// Any other failure (permission denied, timeout, unexpected network
	// error) cannot be classified as either "live" or "confirmed stale".
	// Per the brief: when undecidable, do not clean, and say so loudly
	// rather than guessing.
	return false, false, fmt.Errorf("runtime: recovery: socket probe for %s was undecidable: %w", path, dialErr)
}

// scanPidfile implements HOW step 2. Returns true only when the pidfile
// was confirmed stale (owner pid is ProcessLivenessDead) and removed.
func scanPidfile(path string, log *slog.Logger) bool {
	pid, err := ReadPidfile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false // no pidfile at all: ordinary clean state.
		}
		// Parse error (non-integer content, empty file, etc): log and
		// skip removal — per acceptance criteria, this is reported, not
		// treated as a scan-aborting failure or a silent pass.
		log.Warn("runtime: pidfile present but unparsable; skipping removal", "path", path, "error", err)
		return false
	}

	liveness, aliveErr := ProcessAlive(pid)
	if liveness != ProcessLivenessDead {
		// Alive or Undecided: never remove. This is the PID-recycling
		// defense — a pidfile naming a pid the OS now reports as alive
		// (whether it is truly the original daemon or an unrelated
		// process that has since reused the pid) is never treated as
		// stale.
		log.Warn("runtime: pidfile present but owner pid is not confirmed dead; skipping removal",
			"path", path, "pid", pid, "liveness", liveness, "probe_error", aliveErr)
		return false
	}

	if err := RemovePidfile(path); err != nil {
		log.Warn("runtime: failed to remove confirmed-stale pidfile", "path", path, "pid", pid, "error", err)
		return false
	}
	log.Warn("runtime: removed stale pidfile", "path", path, "pid", pid)
	return true
}

// scanOrphanedLocks implements HOW step 4. A nil registry means the step
// is skipped outright (see DomainRegistry's doc comment) — this is
// reported via the empty return, and the caller's LOCKS-CONSIDERED
// documentation states plainly that no concrete registry is wired in
// production today. Errors from OrphanedLocks itself abort only the
// lock-scanning phase (pidfile/socket cleanup already happened
// independently); a Release error is logged and scanning continues with
// the remaining locks, per acceptance criteria.
func scanOrphanedLocks(ctx context.Context, reg DomainRegistry, log *slog.Logger) []string {
	if reg == nil {
		return nil
	}
	locks, err := reg.OrphanedLocks(ctx)
	if err != nil {
		log.Warn("runtime: recovery: could not list orphaned locks; skipping lock cleanup", "error", err)
		return nil
	}

	var released []string
	for _, l := range locks {
		liveness, _ := ProcessAlive(l.OwnerPID)
		if liveness != ProcessLivenessDead {
			continue
		}
		if err := reg.Release(ctx, l.LockID); err != nil {
			log.Warn("runtime: recovery: failed to release orphaned lock", "lock_id", l.LockID, "owner_pid", l.OwnerPID, "error", err)
			continue
		}
		log.Warn("runtime: recovery: released orphaned advisory lock", "lock_id", l.LockID, "owner_pid", l.OwnerPID)
		released = append(released, l.LockID)
	}
	return released
}

// publishRecoveryEvent encodes ev as JSON and publishes it on the
// "runtime" namespace, mirroring metrics_emitter.go's established
// EventBus usage (namespace/kind/source convention).
func publishRecoveryEvent(ctx context.Context, bus EventBus, ev *RecoveryEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("runtime: recovery: encode RecoveryEvent: %w", err)
	}
	return bus.Publish(ctx, "runtime", "recovery.scan", "runtime.recovery", payload)
}
