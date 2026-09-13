//go:build !windows

// Purpose: the three `cascade backup restore` tests that depend on
// createTestSnapshot (backup_ceremony_helpers_test.go), which runs the
// full real elevation ceremony. That ceremony is architecturally
// unreachable on Windows -- platformElevationRefusal
// (internal/rpc/elevation_windows.go) preempts it before the handler ever
// runs (internal/rpc/elevation_flow_windows_test.go) -- so this file
// carries the same `!windows` tag as the POSIX attestation flow itself
// (elevation_unix.go), per the AGENT-BRIEF's "build tags on test files"
// rule. backup_windows_tier2_test.go is this package's Windows-side proof
// of the refusal (R-14.131).
// SPORT: cmd.cascade.backup-restore/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

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
