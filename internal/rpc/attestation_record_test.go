package rpc

// Purpose: proves a persisted attestation verifies only when signed by the
// trusted key over exactly the recorded params, for an always-elevated verb,
// and that a witness hands back the attestation the middleware verified.
// Inputs: locally generated Ed25519 keys; synthetic params.
// Outputs: none (tests).
// Constraints: no keychain; no nonce ledger (persisted records have none).
// SPORT: internal/rpc recorded attestation verification tests (P1-SEC-04).

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

const recordedParams = `{"config_sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`

func signedRecord(t *testing.T, priv ed25519.PrivateKey, params string) Attestation {
	t.Helper()
	return signedRecordFor(t, priv, "config.accept_loosening", params)
}

func signedRecordFor(t *testing.T, priv ed25519.PrivateKey, method, params string) Attestation {
	t.Helper()
	a := Attestation{RequestID: "r-1", Method: method, ActionHash: hashParams([]byte(params)), Nonce: "n-1", PubkeyFingerprint: "fp-test", IssuedUnix: 1, ExpUnix: 2}
	a.SigB64 = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signedFields(a)))
	return a
}

func recordJSON(t *testing.T, a Attestation) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerifyRecordedAttestation(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)
	trust := MapTrustStore{"fp-test": pub}
	good := recordJSON(t, signedRecord(t, priv, recordedParams))
	// Expired long ago: a persisted record is not refused for age.
	if err := VerifyRecordedAttestation(good, "config.accept_loosening", []byte(recordedParams), trust); err != nil {
		t.Fatalf("valid recorded attestation refused: %v", err)
	}
	withExtra := map[string]any{}
	_ = json.Unmarshal(good, &withExtra)
	withExtra["unsigned_extra"] = "config.accept_loosening"
	extra, _ := json.Marshal(withExtra)
	tampered := signedRecord(t, priv, recordedParams)
	tampered.Nonce = "n-2"
	cases := map[string]struct {
		att    json.RawMessage
		method string
		params string
		trust  TrustStore
	}{
		"other key":           {recordJSON(t, signedRecord(t, otherPriv, recordedParams)), "config.accept_loosening", recordedParams, trust},
		"trust holds other":   {good, "config.accept_loosening", recordedParams, MapTrustStore{"fp-test": otherPub}},
		"other params":        {good, "config.accept_loosening", `{"config_sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`, trust},
		"re-encoded params":   {good, "config.accept_loosening", `{"config_sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}`, trust},
		"tampered field":      {recordJSON(t, tampered), "config.accept_loosening", recordedParams, trust},
		"not elevated verb":   {good, "status.get", recordedParams, trust},
		"unknown field":       {extra, "config.accept_loosening", recordedParams, trust},
		"not json":            {json.RawMessage(`nope`), "config.accept_loosening", recordedParams, trust},
		"nil trust":           {good, "config.accept_loosening", recordedParams, nil},
		"bad sig encoding":    {json.RawMessage(`{"pubkey_fingerprint":"fp-test","sig_b64":"%%%"}`), "config.accept_loosening", recordedParams, trust},
		"unknown fingerprint": {good, "config.accept_loosening", recordedParams, MapTrustStore{}},
	}
	for name, c := range cases {
		if err := VerifyRecordedAttestation(c.att, c.method, []byte(c.params), c.trust); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestWitnessRecordedAttestation(t *testing.T) {
	if _, ok := (VerifiedElevation{}).RecordedAttestation(); ok {
		t.Fatal("a witness with no attestation returned one")
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	a := signedRecord(t, priv, recordedParams)
	w := newVerifiedElevation(NewWitnessIssuer(), "config.accept_loosening", a.ActionHash, time.Unix(1, 0))
	w.attestation = a
	raw, ok := w.RecordedAttestation()
	if !ok {
		t.Fatal("RecordedAttestation reported none")
	}
	var back Attestation
	if err := json.Unmarshal(raw, &back); err != nil || back != a {
		t.Fatalf("round trip = %+v (%v), want %+v", back, err, a)
	}
}

func TestVerifyRecordedAttestationBindsMethod(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	trust := MapTrustStore{"fp-test": pub}
	att := recordJSON(t, signedRecordFor(t, priv, "config.accept_loosening", recordedParams))
	if err := VerifyRecordedAttestation(att, "config.accept_loosening", []byte(recordedParams), trust); err != nil {
		t.Fatalf("own method refused: %v", err)
	}
	// The same signature, over the same params, for another always-elevated verb.
	if err := VerifyRecordedAttestation(att, "vault.get", []byte(recordedParams), trust); err == nil {
		t.Fatal("one signature verified for two methods")
	}
	// A record signed for another method does not verify as this one.
	other := recordJSON(t, signedRecordFor(t, priv, "vault.get", recordedParams))
	if err := VerifyRecordedAttestation(other, "config.accept_loosening", []byte(recordedParams), trust); err == nil {
		t.Fatal("a vault.get signature verified as config.accept_loosening")
	}
	// An old-format record (no signed method) refuses.
	old := recordJSON(t, signedRecordFor(t, priv, "", recordedParams))
	if err := VerifyRecordedAttestation(old, "config.accept_loosening", []byte(recordedParams), trust); err == nil {
		t.Fatal("a pre-method record verified")
	}
}
