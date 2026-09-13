// Purpose: `cascade backup restore [--domain] [--yes]` tests that do not
// require the full real elevation ceremony to succeed. The three tests
// that do (RequiresElevation, UnknownDomain, SucceedsWithRealAttestation)
// live in backup_restore_realceremony_test.go (`!windows`): Windows's
// tier-2 platform refusal (internal/rpc/elevation_windows.go) preempts
// that ceremony before it can ever run createTestSnapshot's fixture setup.
// SPORT: cmd.cascade.backup-restore/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestBackupCLIRestoreNoSnapshots proves restoring a target with zero
// snapshots is a typed not-found refusal, not a panic.
func TestBackupCLIRestoreNoSnapshots(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	addTestFSTarget(t, deps, "primary")
	if _, _, err := runBackup(t, deps, "", "restore", "--yes"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("restore with no snapshots = %v, want KindNotFound", err)
	}
}
