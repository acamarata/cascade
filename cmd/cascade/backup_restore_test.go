// Purpose: `cascade backup restore [--domain] [--yes]` tests.
// Inputs: a real fs target with one real snapshot, a real elevation
// ceremony, real Restore/VerifyIntegrity collaborators.
// Outputs: exercised RunE paths, elevation refusal, and domain validation.
// Constraints: no restore work begins before a proof is minted; no
// positional snapshot-id argument exists.
// SPORT: cmd.cascade.backup-restore/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// createTestSnapshot registers target and takes one real, fully-elevated
// snapshot through the real CLI path, so restore tests exercise a genuine
// signed manifest rather than a hand-built fixture.
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

// TestBackupCLIRestoreRequiresElevation proves restore refuses
// ELEVATION_REQUIRED without a proof, before any restore work.
func TestBackupCLIRestoreRequiresElevation(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	createTestSnapshot(t, deps, "primary")

	restoreDeps := deps
	restoreDeps.Authorize = backupDenyGate{}.authorize
	if _, _, err := runBackup(t, restoreDeps, "", "restore", "--yes"); !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("restore without elevation = %v, want ELEVATION_REQUIRED", err)
	}
}

// TestBackupCLIRestoreUnknownDomain proves an unrecognized --domain value
// is a typed refusal, validated before elevation is even attempted.
func TestBackupCLIRestoreUnknownDomain(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	createTestSnapshot(t, deps, "primary")
	if _, _, err := runBackup(t, deps, "", "restore", "--domain", "not-a-real-domain", "--yes"); !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("restore --domain not-a-real-domain = %v, want KindInvalidInput", err)
	}
}

// TestBackupCLIRestoreSucceedsWithRealAttestation is the Art.2 real-stack
// wiring proof: a genuine elevation ceremony authorizes a real restore of
// the real snapshot createTestSnapshot took.
func TestBackupCLIRestoreSucceedsWithRealAttestation(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	createTestSnapshot(t, deps, "primary")

	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	restoreDeps := deps
	restoreDeps.Authorize = newBackupAuthorizer(elevateDeps)

	if _, _, err := runBackup(t, restoreDeps, "", "restore", "--yes"); err != nil {
		t.Fatalf("restore with a real attestation: %v", err)
	}
}

// TestBackupCLIRestoreNoSnapshots proves restoring a target with zero
// snapshots is a typed not-found refusal, not a panic.
func TestBackupCLIRestoreNoSnapshots(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	addTestFSTarget(t, deps, "primary")
	if _, _, err := runBackup(t, deps, "", "restore", "--yes"); !isCLIKind(err, cascade.KindNotFound) {
		t.Fatalf("restore with no snapshots = %v, want KindNotFound", err)
	}
}
