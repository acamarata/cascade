// Purpose: stale-pid and stale-socket helpers for recovery.go's startup
//   scanner — pidfile parsing (FuzzParsePidfile's target), pidfile/socket
//   removal, and the ProcessLiveness tri-state used to decide whether a
//   pid names a dead, live, or undecidable process.
// Inputs: pidfile bytes (ParsePidfile), filesystem paths (ReadPidfile,
//   RemovePidfile, RemoveSocketFile), a pid (ProcessAlive — platform
//   implementation in lockfile_unix.go / lockfile_windows.go, split per
//   R-14.133: a `//go:build` platform tag is file-scoped, the same
//   footing as that ruling's `//go:build integration` case, so the split
//   joins this ticket's authorized write set automatically).
// Outputs: parsed pids, removal results, and a ProcessLiveness that is
//   Dead ONLY on an unambiguous OS-level "no such process" signal —
//   everything else (Alive, or any error kill(0) cannot classify) reports
//   Undecided/Alive so recovery.go's conservative default (never delete on
//   ambiguity) has something to key off.
// Constraints: no bare time.Now (none needed here — this file is pure
//   filesystem/OS-signal helpers, no timestamps). Art.7.1 — all test
//   writes live under t.TempDir(); FuzzParsePidfile takes only a []byte,
//   no filesystem I/O at all.
// SPORT: runtime/lockfile (ADD, per P1-E03-W1-S05-T3 sport_updates
//   placeholder).

package runtime

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ProcessLiveness is the tri-state result of checking whether a pid names
// a currently-running process. Only ProcessLivenessDead is a confirmed
// "safe to treat as stale" signal; ProcessLivenessAlive and
// ProcessLivenessUndecided both mean "do not delete" to every caller in
// this package — the distinction between them exists only for logging,
// never for the deletion decision.
type ProcessLiveness int

const (
	// ProcessLivenessUndecided means the OS-level probe could not
	// classify the pid one way or the other (e.g. an unexpected syscall
	// error, or a platform with no probe at all). Conservative default:
	// treated identically to Alive by every caller — never delete.
	ProcessLivenessUndecided ProcessLiveness = iota
	// ProcessLivenessAlive means a process currently holds pid. This is
	// reported both for "yes, this exact process exists" and for the
	// permission-denied case (a process exists but this daemon lacks
	// rights to signal it) — POSIX's kill(2) cannot tell those apart
	// without a successful signal 0, and neither should be treated as
	// safe to clean up.
	ProcessLivenessAlive
	// ProcessLivenessDead means the OS unambiguously reported "no such
	// process" (ESRCH) for pid. This is the ONLY result recovery.go's
	// staleness test accepts as license to remove an artifact naming
	// this pid.
	ProcessLivenessDead
)

// ParsePidfile parses the integer pid from pidfile content b. This is
// FuzzParsePidfile's target (lockfile_test.go) — it must never panic on
// any input, including empty, truncated, or adversarial bytes; every
// rejection path returns a plain error.
//
// Rejected: empty (after trimming surrounding whitespace, the pidfile
// convention), non-integer content, and non-positive integers — 0 or a
// negative number is never a valid OS pid, and silently succeeding on one
// would let a corrupt pidfile masquerade as "no stale pid" instead of
// surfacing as the parse error it actually is (Art.1: report, don't
// pretend).
func ParsePidfile(b []byte) (int, error) {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return 0, fmt.Errorf("lockfile: pidfile is empty")
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("lockfile: pidfile content %q is not an integer: %w", s, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("lockfile: pidfile contains non-positive pid %d", pid)
	}
	return pid, nil
}

// ReadPidfile reads and parses the pidfile at path. A missing file is
// reported through the same wrapped os.ErrNotExist os.ReadFile already
// produces — callers use errors.Is(err, os.ErrNotExist) to tell "no
// pidfile at all" (the expected clean-startup case) apart from a genuine
// read or parse failure.
func ReadPidfile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return ParsePidfile(b)
}

// RemovePidfile removes the pidfile at path. A file that is already gone
// is not an error — the caller's goal ("no pidfile present") is already
// satisfied, and idempotent removal is exactly what recovery.go's
// no-op-on-clean-state contract needs.
func RemovePidfile(path string) error {
	return removeIfExists(path)
}

// RemoveSocketFile removes the unix socket file at path. Same
// already-gone-is-fine semantics as RemovePidfile.
func RemoveSocketFile(path string) error {
	return removeIfExists(path)
}

// removeIfExists removes path, treating "already absent" as success
// rather than an error, so repeated calls (recovery.go's idempotency
// contract) never fail on the second, already-clean pass.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
