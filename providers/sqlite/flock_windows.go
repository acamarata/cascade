//go:build windows

// Purpose: windows stand-in for the §D-3 arbitration exclusive lock. Real
//   Windows advisory locking (LockFileEx) is tier-2 scope, out of this
//   ticket per 04-PEWS-PLAN-W1-W3.md's T4 "Headless one-shot embedded
//   runtime (daemonless; Windows path)" note — this file exists so
//   `GOOS=windows go build ./providers/sqlite/...` succeeds and every call
//   site gets a uniform acquireExclusiveLock signature across all three
//   platforms, but it always refuses rather than silently granting a lock
//   it cannot actually enforce.
// Constraints: build-tagged windows-only per files_scope. The refusal
//   message is asserted by a build-tagged test (driver_test.go) so a
//   future edit cannot silently change or drop it.
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).

package sqlite

import "github.com/acamarata/cascade/pkg/cascade"

// windowsLockRefusalMsg is asserted verbatim by driver_test.go's
// build-tagged windows case — keep the two in sync if this message ever
// changes.
const windowsLockRefusalMsg = "sqlite: §D-3 exclusive flock is not yet implemented on windows (tier-2 scope)"

// acquireExclusiveLock always refuses on windows: this ticket does not
// implement real Windows advisory locking (LockFileEx), so rather than
// silently proceeding without the "never two writers" guarantee §D-3
// requires, Open fails closed with an explicit, actionable KindUnsupported
// error.
func acquireExclusiveLock(path string) (unlock func() error, err error) {
	return nil, cascade.Newf(cascade.KindUnsupported, "%s (path=%s)", windowsLockRefusalMsg, path)
}
