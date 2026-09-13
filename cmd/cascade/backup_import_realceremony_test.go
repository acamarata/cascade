//go:build !windows

// Purpose: the three `cascade backup import` tests that need a real
// exported artifact (exportTestArtifact, which calls createTestSnapshot in
// backup_ceremony_helpers_test.go) or a real import ceremony. Both run the
// full real elevation ceremony, which is architecturally unreachable on
// Windows -- platformElevationRefusal (internal/rpc/elevation_windows.go)
// preempts it before the handler ever runs
// (internal/rpc/elevation_flow_windows_test.go) -- so this file carries
// the same `!windows` tag as the POSIX attestation flow itself
// (elevation_unix.go), per the AGENT-BRIEF's "build tags on test files"
// rule. backup_windows_tier2_test.go is this package's Windows-side proof
// of the refusal (R-14.131).
// SPORT: cmd.cascade.backup-import/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// exportTestArtifact creates a real elevated snapshot on srcTarget and
// exports it to a real tar.zst.age artifact, returning its bytes.
func exportTestArtifact(t *testing.T, deps backupDeps, srcTarget string) []byte {
	t.Helper()
	createTestSnapshot(t, deps, srcTarget)
	out := filepath.Join(t.TempDir(), "export.tar.zst.age")
	exportDeps := deps
	exportDeps.Authorize = backupAllowGate{}.authorize
	if _, _, err := runBackup(t, exportDeps, "", "export", "--out", out, "--yes"); err != nil {
		t.Fatalf("exportTestArtifact: export: %v", err)
	}
	artifact, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("exportTestArtifact: read: %v", err)
	}
	return artifact
}

// TestBackupCLIImportRequiresElevation proves import refuses
// ELEVATION_REQUIRED without a proof, before reading the artifact.
func TestBackupCLIImportRequiresElevation(t *testing.T) {
	srcDeps := testBackupDeps(t, backupAllowGate{}.authorize)
	artifact := exportTestArtifact(t, srcDeps, "source")

	destDeps := testBackupDeps(t, backupDenyGate{}.authorize)
	addTestFSTarget(t, destDeps, "dest")
	in := filepath.Join(t.TempDir(), "in.tar.zst.age")
	if err := os.WriteFile(in, artifact, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, _, err := runBackup(t, destDeps, "", "import", "--in", in); !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("import without elevation = %v, want ELEVATION_REQUIRED", err)
	}
}

// TestBackupCLIImportTamperedArtifactRefuses is the gate-failure proof: a
// bit-flipped artifact fails the age AEAD tag (or, if it somehow decrypted,
// VerifyIntegrity) and refuses -- never a partial or best-effort import.
func TestBackupCLIImportTamperedArtifactRefuses(t *testing.T) {
	srcDeps := testBackupDeps(t, backupAllowGate{}.authorize)
	artifact := exportTestArtifact(t, srcDeps, "source")
	if len(artifact) == 0 {
		t.Fatal("exported artifact is empty")
	}
	tampered := append([]byte(nil), artifact...)
	tampered[len(tampered)-1] ^= 0xFF

	destDeps := testBackupDeps(t, backupAllowGate{}.authorize)
	addTestFSTarget(t, destDeps, "dest")
	in := filepath.Join(t.TempDir(), "in.tar.zst.age")
	if err := os.WriteFile(in, tampered, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, _, err := runBackup(t, destDeps, "", "import", "--in", in, "--yes"); err == nil {
		t.Fatal("import of a tampered artifact succeeded, want a refusal")
	}
}

// TestBackupCLIImportSucceedsWithRealAttestation is the Art.2 real-stack
// wiring proof: a genuine elevation ceremony authorizes a real import of a
// real artifact this suite's own `backup export` produced, onto a second,
// independent fs target.
func TestBackupCLIImportSucceedsWithRealAttestation(t *testing.T) {
	srcDeps := testBackupDeps(t, backupAllowGate{}.authorize)
	artifact := exportTestArtifact(t, srcDeps, "source")

	destDeps := testBackupDeps(t, backupAllowGate{}.authorize)
	addTestFSTarget(t, destDeps, "dest")
	in := filepath.Join(t.TempDir(), "in.tar.zst.age")
	if err := os.WriteFile(in, artifact, 0o600); err != nil {
		t.Fatalf("write artifact: %v", err)
	}

	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	importDeps := destDeps
	importDeps.Authorize = newBackupAuthorizer(elevateDeps)

	if _, _, err := runBackup(t, importDeps, "", "import", "--in", in, "--yes"); err != nil {
		t.Fatalf("import with a real attestation: %v", err)
	}
}
