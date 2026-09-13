//go:build !windows

// Purpose: shared test helpers that run the FULL real elevation ceremony
// (a real ed25519 attestation, verified end to end) to produce a genuine
// signed snapshot or artifact fixture for other tests to operate on.
// Shared across backup_restore_realceremony_test.go,
// backup_import_test.go and backup_export_test.go. This ceremony is
// architecturally unreachable on Windows -- platformElevationRefusal
// (internal/rpc/elevation_windows.go) preempts it before the handler ever
// runs, by design (internal/rpc/elevation_flow_windows_test.go) -- so this
// file carries the same `!windows` tag as every caller, per the
// AGENT-BRIEF's "build tags on test files" rule.
// SPORT: cmd.cascade.backup-restore/ADD (tests) (P1-E19-W4-S42-T3).
package main

import "testing"

// createTestSnapshot registers target and takes one real, fully-elevated
// snapshot through the real CLI path, so callers exercise a genuine signed
// manifest rather than a hand-built fixture.
func createTestSnapshot(t *testing.T, deps backupDeps, target string) {
	t.Helper()
	addTestFSTarget(t, deps, target)
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	createDeps := deps
	createDeps.Authorize = newBackupAuthorizer(elevateDeps)
	if _, _, err := runBackup(t, createDeps, "", "create", "--target", target, "--yes"); err != nil {
		t.Fatalf("createTestSnapshot: %v", err)
	}
}
