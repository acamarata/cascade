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
	// KNOWN GAP (CR follow-up, not fixed here — deliberately out of this
	// ticket's scope): os.Stat FOLLOWS symlinks, so a dangling symlink at
	// path (its target removed, the link itself left behind) resolves to
	// ENOENT here exactly like a genuinely missing socket file, and is
	// classified "clean state" — the link itself is never removed, by
	// this scanner or by anything else in the tree. A later
	// net.Listen(path) can then fail with a confusing "address already in
	// use"-shaped error from the stale link, not the clear
	// DAEMON_ALREADY_RUNNING or ENOENT a caller would expect. Left
	// undecided rather than papered over here because "remove any
	// dangling symlink at the socket path" is a real behavior change with
	// its own question (a symlink there could in principle be an
	// operator's deliberate redirect, not just scanner debris) that this
	// ticket's brief never names — it belongs in a scoped follow-up, not
	// a silent addition to the HOW steps this scanner was reviewed
	// against.
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
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
