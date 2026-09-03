package rpc

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// genAttestTestKey generates a locally-generated Ed25519 key pair, per this
// ticket's contract ("W1 tests use a locally generated Ed25519 key written
// into the trust-store record format").
func genAttestTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return pub, priv
}

// signAttestation signs att with priv and fills SigB64 — the mirror of
// what an eventual D/S-07.T6 attestation producer would do.
func signAttestation(priv ed25519.PrivateKey, att Attestation) Attestation {
	sig := ed25519.Sign(priv, signedFields(att))
	att.SigB64 = base64.StdEncoding.EncodeToString(sig)
	return att
}

func newValidAttestation(t *testing.T, priv ed25519.PrivateKey, fingerprint, nonce, paramsHash string, now time.Time) Attestation {
	t.Helper()
	att := Attestation{
		RequestID:         "req-1",
		ActionHash:        paramsHash,
		Nonce:             nonce,
		PubkeyFingerprint: fingerprint,
		IssuedUnix:        now.Unix(),
		ExpUnix:           now.Add(time.Minute).Unix(),
	}
	return signAttestation(priv, att)
}

func TestVerifyAttestation_Valid(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	pub, priv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	att := newValidAttestation(t, priv, "fp1", nonce, "hash1", clock.Now())

	if err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now()); err != nil {
		t.Fatalf("valid attestation rejected: %v", err)
	}
}

func TestVerifyAttestation_TamperedPayload(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	pub, priv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	att := newValidAttestation(t, priv, "fp1", nonce, "hash1", clock.Now())
	att.ActionHash = "tampered-hash" // mutate AFTER signing

	err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("tampered attestation must be rejected")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

func TestVerifyAttestation_WrongKey(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	pub, _ := genAttestTestKey(t)
	_, otherPriv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	// Signed by a DIFFERENT key than the one enrolled under "fp1".
	att := newValidAttestation(t, otherPriv, "fp1", nonce, "hash1", clock.Now())

	err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("attestation signed by the wrong key must be rejected")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

func TestVerifyAttestation_ReplayedNonce(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	pub, priv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	att := newValidAttestation(t, priv, "fp1", nonce, "hash1", clock.Now())

	if err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now()); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("replayed attestation (nonce already consumed) must be rejected")
	}
}

func TestVerifyAttestation_MalformedEncoding(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	pub, priv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	att := newValidAttestation(t, priv, "fp1", nonce, "hash1", clock.Now())
	att.SigB64 = "not valid base64!!!"

	err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("malformed sig_b64 must be rejected")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

func TestVerifyAttestation_UnknownFingerprint(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	_, priv := genAttestTestKey(t)
	trust := MapTrustStore{}
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	att := newValidAttestation(t, priv, "no-such-fp", nonce, "hash1", clock.Now())
	err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("unknown pubkey_fingerprint must be rejected")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestVerifyAttestation_Expired(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	pub, priv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)
	nonce, _ := ledger.Issue("vault.get", "hash1")

	att := Attestation{
		RequestID:         "req-1",
		ActionHash:        "hash1",
		Nonce:             nonce,
		PubkeyFingerprint: "fp1",
		IssuedUnix:        clock.Now().Unix(),
		ExpUnix:           clock.Now().Add(-time.Second).Unix(), // already expired
	}
	att = signAttestation(priv, att)

	err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("expired attestation must be rejected")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindTimeout {
		t.Errorf("kind = %v (ok=%v), want KindTimeout", kind, ok)
	}
}

func TestVerifyAttestation_NonceNeverIssued(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(2000, 0))
	pub, priv := genAttestTestKey(t)
	trust := MapTrustStore{"fp1": pub}
	ledger := NewNonceLedger(clock)

	att := newValidAttestation(t, priv, "fp1", "never-issued-nonce", "hash1", clock.Now())
	err := VerifyAttestation(att, trust, ledger, "vault.get", "hash1", clock.Now())
	if err == nil {
		t.Fatal("an attestation whose nonce was never issued must never be accepted")
	}
}
