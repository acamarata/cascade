// Purpose: `cascade backup import [--vault-passphrase-file]` tests.
// Inputs: a real portable artifact produced by `backup export`, a real
// elevation ceremony, real ImportPortable collaborators.
// Outputs: exercised RunE paths, elevation refusal, and gate-failure
// refusal on a tampered artifact.
// Constraints: no snapshot-id argument exists; a gate failure leaves the
// destination untouched.
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
