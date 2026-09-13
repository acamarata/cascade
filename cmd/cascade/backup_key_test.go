// Purpose: `cascade backup key export|import` tests -- the real elevation
// ceremony, a real (file-vault-backed) secrets.Broker, the location
// invariant running before elevation, §5.8 --passphrase-env parity,
// CASCADE_NO_INPUT structured refusal, and the escrow guard wired through
// to `backup create`'s real refusal/success transition.
// SPORT: cmd.cascade.backup-key/ADD (tests) (P1-E19-W4-S42-T6).
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/backup"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// testKeyVaultDeps extends testBackupDeps with a REAL secrets.Broker over a
// file vault rooted at a fresh t.TempDir() (never the real keychain/data
// dir -- matches vault_test.go's testVaultDeps precedent exactly, reusing
// its alwaysFailRunner to force the hermetic file-vault backend) and a
// real audit.Log over the same in-memory Store, so the ceremony's vault
// writes and escrow record are Art.2 real collaborators, not fakes.
func testKeyVaultDeps(t *testing.T, authorize backupAuthorizeFunc) backupDeps {
	t.Helper()
	deps := testBackupDeps(t, authorize)
	vaultDir := t.TempDir()
	deps.NewVault = func(proof backup.ElevationProof) (*secrets.Broker, error) {
		custody, err := secrets.SelectCustody(secrets.Config{
			Service: "cascade-backup-key-test", Dir: vaultDir, Passphrase: "cli-test-pass",
			Runner: alwaysFailRunner, ForceFileVault: true,
		})
		if err != nil {
			return nil, err
		}
		return secrets.NewBroker(custody, backupProofGate{proof: proof})
	}
	deps.AuditLog = func(store provider.Store) *audit.Log {
		return audit.New(store, deps.Clock, nil)
	}
	return deps
}

func TestBackupCLIKeyExportRequiresElevation(t *testing.T) {
	deps := testKeyVaultDeps(t, backupDenyGate{}.authorize)
	out := filepath.Join(t.TempDir(), "recovery.age")
	_, _, err := runBackup(t, deps, "", "key", "export", "--out", out, "--passphrase-env", "TEST_PASS")
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("key export without elevation = %v, want ELEVATION_REQUIRED", err)
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Fatal("a refused key export still wrote an artifact")
	}
}

// TestBackupCLIKeyExportLocationInvariantRunsBeforeElevation proves the
// location refusal happens even under a DENY gate -- it must fire before
// any attestation is even attempted, per the ceremony's own AC.
func TestBackupCLIKeyExportLocationInvariantRunsBeforeElevation(t *testing.T) {
	deps := testKeyVaultDeps(t, backupDenyGate{}.authorize)
	addTestFSTarget(t, deps, "primary")
	rt, err := deps.Open(context.Background())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	record, err := backup.GetTarget(context.Background(), rt.Store, backupRegistryNamespace, "primary")
	rt.Close()
	if err != nil {
		t.Fatalf("GetTarget: %v", err)
	}
	inside := filepath.Join(record.FSRoot, "recovery.age")
	_, _, err = runBackup(t, deps, "", "key", "export", "--out", inside, "--passphrase-env", "TEST_PASS")
	if !isCLIKind(err, cascade.KindInvalidInput) {
		t.Fatalf("key export inside a target root = %v, want KindInvalidInput (location invariant, not elevation)", err)
	}
}

func TestBackupCLIKeyExportNoInputRequiresPassphraseEnv(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, map[string]string{"CASCADE_NO_INPUT": "1"})
	deps := testKeyVaultDeps(t, newBackupAuthorizer(elevateDeps))
	deps.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	out := filepath.Join(t.TempDir(), "recovery.age")
	_, stderr, err := runBackup(t, deps, "", "key", "export", "--out", out, "--yes")
	if err == nil {
		t.Fatal("key export with CASCADE_NO_INPUT=1 and no --passphrase-env = nil error, want a refusal")
	}
	if stderr != "" && strings.Contains(stderr, "Enter recovery key passphrase") {
		t.Fatal("a passphrase prompt was written under CASCADE_NO_INPUT=1")
	}
}
