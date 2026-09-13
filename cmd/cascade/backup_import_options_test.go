// Purpose: unit coverage for backupImportOptions' branches (no
//
//	passphrase file = vault untouched; a passphrase file wires a real
//	vault adapter through deps.NewVault), which shipped with no direct
//	test of their own.
//
// SPORT: cmd.cascade.backup-import/TEST (P1-E19-W4-S42-T3).
package main

import (
	"testing"

	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/secrets"
)

func TestBackupImportOptions_NoPassphraseFileLeavesVaultUnset(t *testing.T) {
	opts, err := backupImportOptions(backupDeps{}, nil, "", "")
	if err != nil {
		t.Fatalf("backupImportOptions: %v", err)
	}
	if opts.VaultImporter != nil {
		t.Error("backupImportOptions with no passphrase file set VaultImporter, want nil")
	}
}

func TestBackupImportOptions_WithPassphraseFileWiresVault(t *testing.T) {
	deps := backupDeps{
		ReadFile: func(string) ([]byte, error) { return []byte("hunter2\n"), nil },
		NewVault: func(backup.ElevationProof) (*secrets.Broker, error) {
			return secrets.NewBroker(newMemCustody(), backupProofGate{proof: "p"})
		},
	}
	opts, err := backupImportOptions(deps, nil, "/some/path", "p")
	if err != nil {
		t.Fatalf("backupImportOptions: %v", err)
	}
	if opts.VaultImporter == nil {
		t.Error("backupImportOptions with a passphrase file left VaultImporter nil")
	}
	if opts.VaultPassphrase != "hunter2" {
		t.Errorf("VaultPassphrase = %q, want \"hunter2\"", opts.VaultPassphrase)
	}
}
