//go:build integration

// Purpose: split out of drill_integration_test.go to stay under the
// 300-line file cap -- the ceremony-only ("lose the laptop", no env vars
// ever set) leg of S-42.T5's acceptance drill. Confirms
// DEFECT-backup-keys-vault-not-read.md's fix: manifest.go/integrity.go now
// resolve key material from the S-42.T6 ceremony's vault, not the
// environment only. Inputs/outputs/constraints: identical to
// drill_integration_test.go's own header; this file shares that file's
// helpers (drillVaultStore, newDrillBroker, drillMust, openCaptureTestDB,
// seedCaptureRow, readKVRows, drillContextNS) rather than duplicating any
// of them.
// SPORT: internal.backup.drill/CHANGED (DEFECT-backup-keys-vault-not-read fix).

// Package backup doc: see doc.go for the canonical package comment.
package backup

import (
	"context"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/acamarata/cascade/internal/backup/targets"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// drillCeremony runs the real S-42.T6 ceremony (`key export` then `import`)
// on a fresh vault per machine, and returns the recovered identity plus
// both machines' vaults: vaultA (the ceremony's origin machine, holding
// BOTH the age identity and the manifest signing key) and vaultB (the
// simulated recovering machine, holding only the imported age identity --
// exactly what a real restoring machine has, since the manifest signing
// key never needs to leave the machine that creates snapshots).
func drillCeremony(t *testing.T, exportDir, importDir string) (identity string, vaultA, vaultB VaultStore) {
	t.Helper()
	ctx := context.Background()
	vA := drillVaultStore{broker: newDrillBroker(t, exportDir)}
	identity, err := EnsureAgeIdentity(ctx, vA)
	drillMust(t, err, "EnsureAgeIdentity")
	_, err = EnsureManifestSigningKey(ctx, vA)
	drillMust(t, err, "EnsureManifestSigningKey")
	artifact, err := WrapRecoveryKey("drill-passphrase", identity)
	drillMust(t, err, "WrapRecoveryKey")
	vB := drillVaultStore{broker: newDrillBroker(t, importDir)}
	recovered, err := UnwrapRecoveryKey("drill-passphrase", artifact)
	drillMust(t, err, "UnwrapRecoveryKey")
	drillMust(t, vB.Set(ctx, AgeIdentityVaultName, []byte(recovered)), "vaultB.Set")
	return identity, vA, vB
}

// TestBackupDrillCeremonyOnlyNoEnvVarsRestoresTheLaptop: the real ceremony,
// no env var ever set, driving the real sign (CreateSnapshot), verify
// (VerifyIntegrity), and restore (Restore) paths end to end. Flipped from
// this test's former name and pinned assertions
// (...IsBlockedByTheKnownVaultDefect, which asserted
// ErrManifestSigningKeyMissing/ErrAgeIdentityMissing as the CORRECT
// outcome of the then-unfixed defect) now that manifest.go/integrity.go
// resolve from the ceremony's vault first
// (DEFECT-backup-keys-vault-not-read.md). Never set
// AgeIdentityEnvVar/ManifestSigningKeyEnvVar in this test: that would
// prove nothing about the defect this test exists to guard against
// regressing.
func TestBackupDrillCeremonyOnlyNoEnvVarsRestoresTheLaptop(t *testing.T) {
	t.Setenv(AgeIdentityEnvVar, "")
	t.Setenv(ManifestSigningKeyEnvVar, "")
	ctx := context.Background()
	identity, vaultA, vaultB := drillCeremony(t, t.TempDir(), t.TempDir())
	parsedIdentity, err := age.ParseX25519Identity(identity)
	drillMust(t, err, "ParseX25519Identity")
	recipient := parsedIdentity.Recipient().String()
	pub, err := EnsureManifestSigningKey(ctx, vaultA)
	drillMust(t, err, "EnsureManifestSigningKey read-back")

	target, err := targets.NewFSTarget(t.TempDir())
	drillMust(t, err, "NewFSTarget")
	sourceDB := openCaptureTestDB(t)
	seedCaptureRow(t, sourceDB, drillContextNS, "ctx-1", []byte("owner context row"))
	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock: testkit.NewFrozenClock(time.Unix(1_700_000_800, 0)),
		Domains: map[string]Exporter{
			drillContextNS: SQLiteCapture{DB: sourceDB, Domain: storage.DomainContext, Dir: t.TempDir()},
		},
		Vault: vaultA,
	}
	manifest, err := CreateSnapshot(ctx, "drill-create-proof", deps, nil)
	drillMust(t, err, "CreateSnapshot signing from the vault-only ceremony (no env vars)")

	_, gate, err := VerifyIntegrity(ctx, GateOptions{Target: target, PubKey: pub, Vault: vaultA}, manifest.Snapshot)
	drillMust(t, err, "VerifyIntegrity from the vault-only ceremony (no env vars)")
	if gate.ObjectsVerified == 0 {
		t.Fatalf("gate = %+v, want at least one verified object", gate)
	}

	destDB := openCaptureTestDB(t)
	if _, err := Restore(ctx, "drill-restore-proof",
		RestoreOptions{Target: target, DB: destDB, PubKey: pub, Vault: vaultB}, manifest.Snapshot); err != nil {
		t.Fatalf("Restore on the recovering machine's vault-only identity (no env vars): %v", err)
	}
	if rows := readKVRows(t, destDB, drillContextNS); len(rows) == 0 {
		t.Fatal("restored destination has zero rows after a successful vault-only restore")
	}
}
