// Purpose: tests for the 06 §5.14 nonce+attestation+middleware ceremony
// newBackupAuthorizer runs (backup_elevation.go).
// Inputs: fake ElevationKeystore/Backend collaborators, one real ed25519
// keypair for the success path.
// Outputs: exercised confirm/attest/verify paths.
// Constraints: CASCADE_NO_INPUT never prompts; a valid attestation actually
// verifies against a real signature, never a hardcoded "always allow".
// SPORT: cmd.cascade.backup-elevation/ADD (tests) (P1-E19-W4-S42-T3).
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// signingKeystore signs with a real ed25519 private key, so an attestation
// it produces actually verifies against its matching public key -- unlike
// vault_elevated_test.go's availableKeystore, whose Sign returns nil and is
// only ever used against the daemonless policy check, never real
// cryptographic verification.
type signingKeystore struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func newSigningKeystore(t *testing.T) signingKeystore {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return signingKeystore{priv: priv, pub: pub}
}

func (k signingKeystore) GenerateKey() error { return nil }
func (k signingKeystore) PubKeyB64() (string, error) {
	return base64.StdEncoding.EncodeToString(k.pub), nil
}
func (k signingKeystore) Sign(data []byte) ([]byte, error) { return ed25519.Sign(k.priv, data), nil }
func (k signingKeystore) IsAvailable() bool                { return true }
func (k signingKeystore) Tier() elevation.StorageTier      { return elevation.TierOSKeychain }

// windowsTier2Keystore reports the tier2 storage class, so backupKeystore
// refuses before any attestation attempt.
type windowsTier2Keystore struct{ signingKeystore }

func (windowsTier2Keystore) Tier() elevation.StorageTier { return elevation.TierWindowsTier2 }

// enrolledSigningBackend reports the signing keystore's OWN public key as
// enrolled, so a real attestation from it actually verifies.
type enrolledSigningBackend struct{ pubKeyB64 string }

func (b enrolledSigningBackend) Load() (elevation.TrustRecord, bool, error) {
	return elevation.TrustRecord{PubKeyB64: b.pubKeyB64}, true, nil
}
func (enrolledSigningBackend) Save(elevation.TrustRecord) error { return nil }

// testElevateHelperDeps builds elevateHelperDeps over ks/backend, with an
// injectable env map -- the same shape productionBackupDeps wires
// newBackupAuthorizer with, minus the real Keychain/PAM probe.
func testElevateHelperDeps(ks elevation.ElevationKeystore, backend elevation.Backend, env map[string]string) elevateHelperDeps {
	return elevateHelperDeps{
		Keystore:     func() elevation.ElevationKeystore { return ks },
		TrustBackend: func() elevation.Backend { return backend },
		Clock:        runtime.NewSystemClock(),
		Getenv:       func(k string) string { return env[k] },
	}
}

// TestBackupAuthorizerNoInputMissingYes is the acceptance criterion:
// CASCADE_NO_INPUT=1 with no --yes exits with a structured error and never
// attempts the confirmation prompt (a prompt would block forever on the
// empty stdin this test supplies).
func TestBackupAuthorizerNoInputMissingYes(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	deps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, map[string]string{"CASCADE_NO_INPUT": "1"})
	authorize := newBackupAuthorizer(deps)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	_, err := authorize(t.Context(), cmd, "backup.create", []byte(`{"target":"t"}`), false)
	if !isCLIKind(err, cascade.KindElevationRequired) {
		t.Fatalf("authorize with CASCADE_NO_INPUT=1 and no --yes = %v, want KindElevationRequired", err)
	}
	if !strings.Contains(err.Error(), "CASCADE_NO_INPUT") {
		t.Fatalf("error %v does not name CASCADE_NO_INPUT as the reason", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a confirmation prompt was written under CASCADE_NO_INPUT=1: %q", stderr.String())
	}
}

// TestBackupAuthorizerWindowsTier2 proves a tier-2 keystore refuses before
// any attestation attempt, typed as ErrWindowsTier2.
func TestBackupAuthorizerWindowsTier2(t *testing.T) {
	ks := windowsTier2Keystore{newSigningKeystore(t)}
	deps := testElevateHelperDeps(ks, enrolledSigningBackend{}, nil)
	authorize := newBackupAuthorizer(deps)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	_, err := authorize(t.Context(), cmd, "backup.create", []byte(`{}`), true)
	if !isCLIKind(err, cascade.KindUnsupported) {
		t.Fatalf("authorize with a tier-2 keystore = %v, want the ErrWindowsTier2 refusal", err)
	}
}

// TestBackupAuthorizerUnconfirmed proves a "no" answer (or EOF) at the
// confirmation prompt refuses as permission-denied, without --yes and
// without CASCADE_NO_INPUT.
func TestBackupAuthorizerUnconfirmed(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	deps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	authorize := newBackupAuthorizer(deps)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader("n\n"))
	cmd.SetErr(&strings.Builder{})
	_, err := authorize(t.Context(), cmd, "backup.create", []byte(`{}`), false)
	if !isCLIKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("authorize answered \"n\" = %v, want KindPermissionDenied", err)
	}
}

// TestBackupAuthorizerRealAttestationVerifies is the success-path proof:
// with --yes, a real ed25519 signature from an enrolled key round-trips
// through the nonce challenge, rpc.ElevationMiddleware's real verification,
// and the single-use ledger, minting a non-empty ElevationProof. Swapping
// the enrolled public key for an unrelated one (below) proves the
// verification is real, not a hardcoded pass.
func TestBackupAuthorizerRealAttestationVerifies(t *testing.T) {
	ks := newSigningKeystore(t)
	pubB64, _ := ks.PubKeyB64()
	deps := testElevateHelperDeps(ks, enrolledSigningBackend{pubKeyB64: pubB64}, nil)
	authorize := newBackupAuthorizer(deps)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	proof, err := authorize(t.Context(), cmd, "backup.create", []byte(`{"target":"t"}`), true)
	if err != nil {
		t.Fatalf("authorize with --yes and a real enrolled signature: %v", err)
	}
	if proof == "" {
		t.Fatal("authorize returned an empty proof on a verified attestation")
	}
}

// TestBackupAuthorizerRejectsUnenrolledKey is the RED half of the above
// proof: an attestation signed by a DIFFERENT key than the one enrolled in
// the trust store must fail verification, never mint a proof.
func TestBackupAuthorizerRejectsUnenrolledKey(t *testing.T) {
	signer := newSigningKeystore(t)
	other := newSigningKeystore(t)
	otherB64, _ := other.PubKeyB64()
	deps := testElevateHelperDeps(signer, enrolledSigningBackend{pubKeyB64: otherB64}, nil)
	authorize := newBackupAuthorizer(deps)
	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(""))
	_, err := authorize(t.Context(), cmd, "backup.create", []byte(`{"target":"t"}`), true)
	if err == nil {
		t.Fatal("an attestation signed by a key other than the enrolled one was accepted")
	}
}
