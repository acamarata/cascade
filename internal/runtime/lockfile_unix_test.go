//go:build darwin || linux

// Purpose: coverage for lockfile_unix.go's ProcessAlive fail-safe
//   branches — EPERM ("exists, but I can't prove it's mine") and the
//   Undecided/default branch (an unexpected, unclassifiable kill(2)
//   result). These are the "when in doubt, do not clean" paths
//   recovery.go's entire staleness contract rests on, named explicitly
//   in the CR that reopened this ticket. Split into its own
//   `darwin || linux` file rather than added to lockfile_test.go because
//   the Undecided case exercises killFn, a unix-only seam (see
//   lockfile_unix.go's doc comment on killFn) — the same platform-split
//   rationale R-14.133 already established for a build-tagged test file.
// Constraints: Art.7.1 — no filesystem writes here at all (ProcessAlive
//   takes only a pid). Art.1 — killFn is restored via t.Cleanup after
//   every override so no other test in this package observes a faked
//   kill(2) result.

package runtime

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

// TestProcessAlive_PermissionDeniedIsAlive proves the EPERM path: pid 1
// (init/launchd) always exists but an unprivileged test process cannot
// signal it, so kill(1, 0) reliably returns EPERM on both darwin and
// linux CI runners. Reported as Alive, never Dead — "exists but I can't
// prove it's mine" must never license cleanup.
func TestProcessAlive_PermissionDeniedIsAlive(t *testing.T) {
	if unix.Getuid() == 0 {
		t.Skip("running as root: kill(1, 0) would succeed, not EPERM")
	}
	liveness, err := ProcessAlive(1)
	if err != nil {
		if !errors.Is(err, unix.ESRCH) {
			t.Fatalf("ProcessAlive(1): unexpected error %v (want nil, or ESRCH if pid 1 truly doesn't exist here)", err)
		}
		t.Skip("pid 1 does not exist in this environment (unusual sandbox); EPERM path not exercisable here")
	}
	if liveness != ProcessLivenessAlive {
		t.Fatalf("ProcessAlive(1) = %v, want ProcessLivenessAlive (EPERM reported as Alive, never Dead)", liveness)
	}
}

// TestProcessAlive_UnexpectedErrnoIsUndecided proves the fail-safe
// default branch: a kill(2) result that is neither nil, ESRCH, nor EPERM
// is reported as ProcessLivenessUndecided, not folded into either
// decided outcome. No real, unprivileged kill(pid, 0) call can produce
// such a result (see killFn's doc comment), so this drives the branch
// through the injectable killFn seam — confined to this test file, reset
// via t.Cleanup so no other test observes the override.
func TestProcessAlive_UnexpectedErrnoIsUndecided(t *testing.T) {
	original := killFn
	t.Cleanup(func() { killFn = original })

	wantErr := unix.EIO // an errno kill(2) never actually returns for signal 0
	killFn = func(pid int, _ unix.Signal) error {
		if pid != 4242 {
			t.Fatalf("killFn called with pid %d, want 4242", pid)
		}
		return wantErr
	}

	liveness, err := ProcessAlive(4242)
	if err == nil {
		t.Fatal("ProcessAlive with an unclassifiable kill(2) error: want error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("ProcessAlive error = %v, want it to wrap %v", err, wantErr)
	}
	if liveness != ProcessLivenessUndecided {
		t.Fatalf("ProcessAlive with an unclassifiable kill(2) error = %v, want ProcessLivenessUndecided (never delete on ambiguity)", liveness)
	}
}
