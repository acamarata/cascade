package rpc

// Purpose (this file): the signed attestation names the RPC method it
//   authorizes. One attestation never verifies for another verb whose params
//   are byte-identical, on a live dispatch or as a persisted record; editing
//   the method on the wire breaks the signature; the pre-method format is
//   refused (P1-BF-R266).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const bindArgs = `{"config_sha256":"abc"}`

// bindFixture is one trusted key, a ledger and a fixed clock.
type bindFixture struct {
	clock  *runtime.FixedClock
	priv   ed25519.PrivateKey
	trust  MapTrustStore
	ledger *NonceLedger
	hash   string
}

func newBindFixture(t *testing.T) *bindFixture {
	t.Helper()
	pub, priv := genAttestTestKey(t)
	clock := runtime.NewFixedClock(time.Unix(5000, 0))
	return &bindFixture{clock: clock, priv: priv, trust: MapTrustStore{"fp1": pub},
		ledger: NewNonceLedger(clock), hash: ParamsHash(json.RawMessage(bindArgs))}
}

// sign signs an attestation for method over the fixture params under nonce.
func (f *bindFixture) sign(method, nonce string) Attestation {
	return signAttestation(f.priv, Attestation{RequestID: "req", ActionHash: f.hash, Method: method, Nonce: nonce,
		PubkeyFingerprint: "fp1", IssuedUnix: f.clock.Now().Unix(), ExpUnix: f.clock.Now().Add(time.Minute).Unix()})
}

func (f *bindFixture) issue(t *testing.T, method string) string {
	t.Helper()
	nonce, err := f.ledger.Issue(method, f.hash)
	if err != nil {
		t.Fatal(err)
	}
	return nonce
}

// wantMethodRefusal requires the refusal to come from the signed method check.
func wantMethodRefusal(t *testing.T, err error) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "method") {
		t.Fatalf("err = %v, want the KindInvalidInput method refusal", err)
	}
}

func TestAttestationSignedBytesIncludeMethod(t *testing.T) {
	a := Attestation{RequestID: "r", ActionHash: "h", Method: "plugin.add", Nonce: "n", PubkeyFingerprint: "fp",
		SigB64: "ignored", IssuedUnix: 1, ExpUnix: 2}
	const want = `{"action_hash":"h","exp_unix":2,"issued_unix":1,"method":"plugin.add","nonce":"n","pubkey_fingerprint":"fp","request_id":"r"}`
	if got := string(AttestationSignedBytes(a)); got != want {
		t.Fatalf("signed bytes = %s\nwant          %s", got, want)
	}
}

// TestVerifyAttestationSignedMethodBindsLive: the ledger binds the nonce to
// the dispatched verb, but the signed method names another verb, so the
// attestation is refused before the nonce is spent.
func TestVerifyAttestationSignedMethodBindsLive(t *testing.T) {
	f := newBindFixture(t)
	nonce := f.issue(t, "plugin.add")
	wantMethodRefusal(t, VerifyAttestation(f.sign("plugin.update", nonce), f.trust, f.ledger, "plugin.add", f.hash, f.clock.Now()))
	if err := VerifyAttestation(f.sign("plugin.add", nonce), f.trust, f.ledger, "plugin.add", f.hash, f.clock.Now()); err != nil {
		t.Fatalf("the matching attestation was refused (nonce spent by the refused one?): %v", err)
	}
}

// TestMiddlewareRefusesAttestationSignedForOtherVerb drives the real
// middleware: a challenge for plugin.add answered with an attestation signed
// for plugin.update over identical params never reaches the handler.
func TestMiddlewareRefusesAttestationSignedForOtherVerb(t *testing.T) {
	skipIfPlatformRefuses(t)
	f := newMWFixture(t, "plugin.add")
	nonce := f.challenge(t, bindArgs)
	_, err := f.wrapped(context.Background(), f.envelopeFor(t, "plugin.update", bindArgs, nonce))
	wantMethodRefusal(t, err)
	if f.calls != 0 {
		t.Fatalf("handler ran %d times for an attestation signed for another verb", f.calls)
	}
	nonce = f.challenge(t, bindArgs)
	if _, err := f.wrapped(context.Background(), f.envelope(t, bindArgs, nonce)); err != nil || f.calls != 1 {
		t.Fatalf("matching attestation: err=%v calls=%d", err, f.calls)
	}
}

// TestRecordedAttestationBindsMethod: with no ledger, one recorded
// attestation verifies only for the verb it was signed for.
func TestRecordedAttestationBindsMethod(t *testing.T) {
	f := newBindFixture(t)
	att := f.sign("config.accept_loosening", "recorded-nonce")
	if err := verifySignedAttestation(att, f.trust, "config.accept_loosening", f.hash); err != nil {
		t.Fatalf("record for its own verb: %v", err)
	}
	wantMethodRefusal(t, verifySignedAttestation(att, f.trust, "plugin.add", f.hash))
	wantMethodRefusal(t, verifySignedAttestation(att, f.trust, "", f.hash))
}

// TestAttestationMethodTamperBreaksSignature edits the method in the wire
// form after signing; both the live and the record check refuse it as a
// signature failure.
func TestAttestationMethodTamperBreaksSignature(t *testing.T) {
	f := newBindFixture(t)
	nonce := f.issue(t, "plugin.update")
	wire, err := json.Marshal(f.sign("plugin.add", nonce))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(wire), `"method":"plugin.add"`, `"method":"plugin.update"`, 1)
	if edited == string(wire) {
		t.Fatalf("wire form carries no method field: %s", wire)
	}
	var att Attestation
	if err := json.Unmarshal([]byte(edited), &att); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"live":   VerifyAttestation(att, f.trust, f.ledger, "plugin.update", f.hash, f.clock.Now()),
		"record": verifySignedAttestation(att, f.trust, "plugin.update", f.hash),
	} {
		if !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("%s: err = %v, want a KindIntegrity signature failure", name, err)
		}
	}
}

// TestPreMethodAttestationRefused: an attestation signed over the earlier
// six-field bytes (no method) is refused, with or without a method added to
// its wire form afterwards, and a signer that signs an empty method is
// refused too.
func TestPreMethodAttestationRefused(t *testing.T) {
	f := newBindFixture(t)
	nonce := f.issue(t, "plugin.add")
	legacy, _ := json.Marshal(struct {
		ActionHash        string `json:"action_hash"`
		ExpUnix           int64  `json:"exp_unix"`
		IssuedUnix        int64  `json:"issued_unix"`
		Nonce             string `json:"nonce"`
		PubkeyFingerprint string `json:"pubkey_fingerprint"`
		RequestID         string `json:"request_id"`
	}{f.hash, f.clock.Now().Add(time.Minute).Unix(), f.clock.Now().Unix(), nonce, "fp1", "req"})
	old := Attestation{RequestID: "req", ActionHash: f.hash, Nonce: nonce, PubkeyFingerprint: "fp1",
		IssuedUnix: f.clock.Now().Unix(), ExpUnix: f.clock.Now().Add(time.Minute).Unix(),
		SigB64: base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, legacy))}
	if bytes.Equal(AttestationSignedBytes(old), legacy) {
		t.Fatal("empty-method signed bytes equal the legacy six-field bytes")
	}
	withMethod := old
	withMethod.Method = "plugin.add"
	for name, att := range map[string]Attestation{"no method": old, "method added": withMethod} {
		if err := VerifyAttestation(att, f.trust, f.ledger, "plugin.add", f.hash, f.clock.Now()); err == nil {
			t.Errorf("%s: live verification accepted a pre-method attestation", name)
		}
		if err := verifySignedAttestation(att, f.trust, "plugin.add", f.hash); err == nil {
			t.Errorf("%s: record verification accepted a pre-method attestation", name)
		}
	}
	empty := f.sign("", nonce)
	wantMethodRefusal(t, VerifyAttestation(empty, f.trust, f.ledger, "plugin.add", f.hash, f.clock.Now()))
	wantMethodRefusal(t, verifySignedAttestation(empty, f.trust, "plugin.add", f.hash))
	wantMethodRefusal(t, verifySignedAttestation(empty, f.trust, "", f.hash))
}

// TestInvalidUTF8MethodRefused: json.Marshal rewrites invalid UTF-8 to U+FFFD,
// so two different invalid methods would sign the same bytes; a signed method
// that is not valid UTF-8 is refused on both paths.
func TestInvalidUTF8MethodRefused(t *testing.T) {
	f := newBindFixture(t)
	const bad = "plugin.add\xff"
	a, b := Attestation{Method: bad}, Attestation{Method: "plugin.add\xfe"}
	if !bytes.Equal(AttestationSignedBytes(a), AttestationSignedBytes(b)) {
		t.Fatal("fixture premise: json.Marshal no longer folds invalid UTF-8; revisit this test")
	}
	nonce := f.issue(t, bad)
	att := f.sign(bad, nonce)
	wantMethodRefusal(t, VerifyAttestation(att, f.trust, f.ledger, bad, f.hash, f.clock.Now()))
	wantMethodRefusal(t, verifySignedAttestation(att, f.trust, bad, f.hash))
}
