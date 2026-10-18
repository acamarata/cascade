//go:build !windows

// Purpose: the helper keystore's signature is accepted by the same
//
//	attestation verifier the daemon uses (internal/rpc), over the key the fake
//	helper reports. External test package: internal/rpc imports this package.
//
// SPORT: internal/elevation helper custody tests/ADDED (P1-SHIP-15).
package elevation_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// signable mirrors the canonical attestation bytes (alphabetical JSON keys).
type signable struct {
	ActionHash        string `json:"action_hash"`
	ExpUnix           int64  `json:"exp_unix"`
	IssuedUnix        int64  `json:"issued_unix"`
	Nonce             string `json:"nonce"`
	PubkeyFingerprint string `json:"pubkey_fingerprint"`
	RequestID         string `json:"request_id"`
}

func TestHelperKeystoreSignRoundTrip(t *testing.T) {
	elevation.TrustAnyVolumeForTest(t)
	dir := t.TempDir()
	exe := filepath.Join(dir, "cascade")
	elevation.WriteFileForTest(t, exe)
	elevation.BuildFakeHelperForTest(t, dir)
	ks := elevation.NewHelperKeystoreForTest(func() (string, error) { return exe, nil }, elevation.OKVerifier{})
	if !ks.IsAvailable() || ks.Tier() != elevation.TierOSKeychain {
		t.Fatal("fake helper is not available as an os-keychain keystore")
	}
	pubB64, err := ks.PubKeyB64()
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := base64.StdEncoding.DecodeString(pubB64)
	fp, err := elevation.Fingerprint(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2000, 0)
	ledger := rpc.NewNonceLedger(runtime.NewFixedClock(now))
	nonce, err := ledger.Issue("vault.get", "hash1")
	if err != nil {
		t.Fatal(err)
	}
	att := rpc.Attestation{RequestID: "req-1", ActionHash: "hash1", Nonce: nonce, PubkeyFingerprint: fp,
		IssuedUnix: now.Unix(), ExpUnix: now.Add(time.Minute).Unix()}
	payload, _ := json.Marshal(signable{att.ActionHash, att.ExpUnix, att.IssuedUnix, att.Nonce, att.PubkeyFingerprint, att.RequestID})
	sig, err := ks.Sign(payload)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !ed25519.Verify(pub, payload, sig) {
		t.Fatal("signature does not verify over the helper's public key")
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	trust := rpc.MapTrustStore{fp: pub}
	if err := rpc.VerifyAttestation(att, trust, ledger, "vault.get", "hash1", now); err != nil {
		t.Fatalf("daemon verifier rejected the helper's attestation: %v", err)
	}
	if got := elevation.HelperPromptReason(payload); got != "Cascade elevation "+digest12(payload) {
		t.Fatalf("prompt = %q", got)
	}
}

func digest12(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}
