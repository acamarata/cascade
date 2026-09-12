// Purpose: `cascade backup export [--include-vault]` tests.
// Inputs: a real fs target with one real snapshot, a real elevation
// ceremony, real ExportPortable/ImportPortable collaborators.
// Outputs: exercised RunE paths, elevation refusal, and the §D-34 opt-in
// default-off proof driven end to end through the real CLI.
// Constraints: default export carries no vault material; --include-vault
// with no --vault-passphrase-file refuses rather than silently proceeding
// unwrapped.
// SPORT: cmd.cascade.backup-export/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestBackupCLIExportRequiresElevation proves export refuses
// ELEVATION_REQUIRED without a proof, before any target read.
func TestBackupCLIExportRequiresElevation(t *testing.T) {
	deps := testBackupDeps(t, backupDenyGate{}.authorize)
	createTestSnapshot(t, deps, "primary")

	exportDeps := deps
	exportDeps.Authorize = backupDenyGate{}.authorize
	out := filepath.Join(t.TempDir(), "export.tar.zst.age")
	if _, _, err := runBackup(t, exportDeps, "", "export", "--out", out); !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("export without elevation = %v, want ELEVATION_REQUIRED", err)
	}
}

// TestBackupCLIExportDefaultCarriesNoVaultMaterial is the acceptance
// criterion (§D-34): the default `backup export`, with no --include-vault,
// produces an artifact with no vault member at all. Proven end to end
// through the real CLI, not the package-level unit test alone: the export
// runs through runBackupExport with a real elevation proof, and the
// resulting artifact bytes are handed to the real ImportPortable (also
// elevated) against a second destination target, asserting
// ImportReport.VaultImported is false -- the same field
// internal/backup/vaultexport.go's own opt-in proof relies on, now proven
// from the actual CLI entry point outward.
func TestBackupCLIExportDefaultCarriesNoVaultMaterial(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	createTestSnapshot(t, deps, "primary")

	out := filepath.Join(t.TempDir(), "export.tar.zst.age")
	if _, _, err := runBackup(t, deps, "", "export", "--out", out, "--yes"); err != nil {
		t.Fatalf("export: %v", err)
	}

	artifact, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the exported artifact: %v", err)
	}
	if len(artifact) == 0 {
		t.Fatal("export wrote an empty artifact")
	}

	destRoot := t.TempDir()
	destRecord := backup.TargetRecord{Name: "dest", Kind: backup.TargetKindFS, FSRoot: destRoot}
	destTarget, err := backup.BuildTarget(t.Context(), destRecord, nil, func(string) string { return "" })
	if err != nil {
		t.Fatalf("BuildTarget(dest): %v", err)
	}
	report, err := backup.ImportPortable(t.Context(), "test-import-proof", backup.ImportOptions{Dest: destTarget}, artifact, "")
	if err != nil {
		t.Fatalf("ImportPortable on the CLI's own export output: %v", err)
	}
	if report.VaultImported {
		t.Fatal("a default (--include-vault omitted) CLI export round-tripped a vault member")
	}
}

// TestBackupCLIExportIncludeVaultRequiresPassphraseFile proves opting in to
// vault export with no --vault-passphrase-file refuses closed, never
// silently proceeding without one.
func TestBackupCLIExportIncludeVaultRequiresPassphraseFile(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	createTestSnapshot(t, deps, "primary")
	out := filepath.Join(t.TempDir(), "export.tar.zst.age")
	_, _, err := runBackup(t, deps, "", "export", "--out", out, "--include-vault", "--yes")
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("export --include-vault with no passphrase file = %v, want KindInvalidInput", err)
	}
}

// TestBackupCLIExportPassphraseFileWithoutIncludeVault proves the flag
// combination is validated up front: a passphrase file with no
// --include-vault is a usage error, never a silently-ignored flag.
func TestBackupCLIExportPassphraseFileWithoutIncludeVault(t *testing.T) {
	deps := testBackupDeps(t, backupAllowGate{}.authorize)
	createTestSnapshot(t, deps, "primary")
	passFile := filepath.Join(t.TempDir(), "pass.txt")
	if err := os.WriteFile(passFile, []byte("hunter2\n"), 0o600); err != nil {
		t.Fatalf("write passphrase file: %v", err)
	}
	out := filepath.Join(t.TempDir(), "export.tar.zst.age")
	_, _, err := runBackup(t, deps, "", "export", "--out", out, "--vault-passphrase-file", passFile, "--yes")
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("export --vault-passphrase-file with no --include-vault = %v, want KindInvalidInput", err)
	}
}
