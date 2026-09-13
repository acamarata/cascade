//go:build !windows

// Purpose: the three `cascade backup key`/`backup create` tests that
// require the FULL real elevation ceremony to actually succeed (a real
// ed25519 attestation verified end-to-end) or to run more than once in a
// row. On Windows, platformElevationRefusal (internal/rpc/elevation_windows.go)
// preempts the attestation flow before it ever runs, by design
// (internal/rpc/elevation_flow_windows_test.go); no keystore fake can
// change that, since it is a compile-time platform gate, not a data-level
// one. These three tests therefore carry the same `!windows` tag as the
// POSIX attestation flow itself (elevation_unix.go) -- moved out of
// backup_key_test.go rather than merely skipped, per the AGENT-BRIEF's
// "build tags on test files" rule: a test exercising tagged code needs the
// matching tag, not a runtime skip. backup_windows_tier2_test.go is this
// package's Windows-side proof that the refusal itself is correct
// (R-14.131), so Windows coverage of the backup-key ceremony is not lost,
// only its POSIX-only "succeeds" assertions.
// SPORT: cmd.cascade.backup-key/ADD (tests) (P1-E19-W4-S42-T6).
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestBackupCLIKeyExportImportRoundTrip is the end-to-end Art.2 proof: a
// real elevation ceremony exports a real vault-held age identity,
// passphrase-wrapped, and a second real elevation ceremony imports it back
// through the real CLI -- proving the artifact this command writes is
// exactly what its own import command accepts.
func TestBackupCLIKeyExportImportRoundTrip(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	deps := testKeyVaultDeps(t, newBackupAuthorizer(elevateDeps))

	out := filepath.Join(t.TempDir(), "recovery.age")
	stdin := "a-real-passphrase\n"
	stdout, _, err := runBackup(t, deps, stdin, "key", "export", "--out", out, "--yes")
	if err != nil {
		t.Fatalf("key export: %v", err)
	}
	if !strings.Contains(stdout, "manifest_signing_pubkey") {
		t.Fatalf("key export result did not report a manifest_signing_pubkey: %s", stdout)
	}
	artifact, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading the exported artifact: %v", err)
	}
	if len(artifact) == 0 {
		t.Fatal("key export wrote an empty artifact")
	}

	if _, _, err := runBackup(t, deps, stdin, "key", "import", out, "--yes"); err != nil {
		t.Fatalf("key import: %v", err)
	}
}

// TestBackupCLIKeyImportWrongPassphraseRefuses proves the CLI import path
// propagates UnwrapRecoveryKey's fail-closed refusal, never loading
// anything into the vault on a wrong passphrase.
func TestBackupCLIKeyImportWrongPassphraseRefuses(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	deps := testKeyVaultDeps(t, newBackupAuthorizer(elevateDeps))

	out := filepath.Join(t.TempDir(), "recovery.age")
	if _, _, err := runBackup(t, deps, "right-passphrase\n", "key", "export", "--out", out, "--yes"); err != nil {
		t.Fatalf("key export: %v", err)
	}
	if _, _, err := runBackup(t, deps, "wrong-passphrase\n", "key", "import", out, "--yes"); !isCLIKind(err, cascade.KindIntegrity) {
		t.Fatalf("key import with wrong passphrase = %v, want KindIntegrity", err)
	}
}

// TestBackupCLICreateEscrowGuardWiredEndToEnd is the wiring-mutation proof
// the AGENT-BRIEF requires: `backup create` refuses BEFORE the ceremony
// has run, and succeeds after it, through the real command tree -- proving
// backup_create.go's Escrow wiring is not a no-op. (The RED case -- remove
// the wiring, see the refusal disappear -- was exercised manually against
// backup_create.go's `Escrow: backupEscrowChecker(deps, rt)` line during
// development; see the journal.)
func TestBackupCLICreateEscrowGuardWiredEndToEnd(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	elevateDeps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	deps := testKeyVaultDeps(t, newBackupAuthorizer(elevateDeps))
	addTestFSTarget(t, deps, "primary")

	_, _, err := runBackup(t, deps, "", "create", "--target", "primary", "--yes")
	if !isCLIKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("create before the recovery-key ceremony = %v, want KindPermissionDenied (ErrBackupKeyNotEscrowed)", err)
	}

	out := filepath.Join(t.TempDir(), "recovery.age")
	if _, _, err := runBackup(t, deps, "a-passphrase\n", "key", "export", "--out", out, "--yes"); err != nil {
		t.Fatalf("key export: %v", err)
	}

	if _, _, err := runBackup(t, deps, "", "create", "--target", "primary", "--yes"); err != nil {
		t.Fatalf("create after the recovery-key ceremony: %v", err)
	}
}
