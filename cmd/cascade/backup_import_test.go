// Purpose: `cascade backup import [--vault-passphrase-file]` tests that do
// not require a real elevation ceremony or a real exported artifact. The
// three tests that do (RequiresElevation, TamperedArtifactRefuses,
// SucceedsWithRealAttestation) live in
// backup_import_realceremony_test.go (`!windows`): Windows's tier-2
// platform refusal (internal/rpc/elevation_windows.go) preempts the
// ceremony exportTestArtifact needs before it can ever run.
// SPORT: cmd.cascade.backup-import/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestBackupCLIImportMissingFile proves a nonexistent --in path is a typed
// refusal, not a panic.
func TestBackupCLIImportMissingFile(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	addTestFSTarget(t, deps, "dest")
	_, _, err := runBackup(t, deps, "", "import", "--in", filepath.Join(t.TempDir(), "absent.tar.zst.age"), "--yes")
	if !isCLIKind(err, cascade.KindUnavailable) {
		t.Fatalf("import of a missing file = %v, want KindUnavailable", err)
	}
}
