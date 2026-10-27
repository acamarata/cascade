// Purpose: the signers sign the method. `elevate-helper --sign` takes the
//
//	method as a protocol argument and its attestation verifies only for that
//	method; the old three-argument form is refused; signAttestation (the CLI
//	and backup primitive) binds the method it is given (P1-BF-R266).
//
// SPORT: cmd/cascade elevate-helper method binding tests/ADDED (P1-SEC-03).
package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// wantSignedMethodRefusal requires the verifier's signed-method refusal.
func wantSignedMethodRefusal(t *testing.T, err error) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "method") {
		t.Fatalf("err = %v, want the KindInvalidInput method refusal", err)
	}
}

func TestElevateHelperSignBindsMethod(t *testing.T) {
	ks := &fakeCmdKeystore{}
	if err := ks.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	deps := newTestDeps(t, ks, nil)
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	deps.Clock = clock
	ledger := rpc.NewNonceLedger(clock)
	nonce, err := ledger.Issue("vault.rotate", "hash-same")
	if err != nil {
		t.Fatal(err)
	}
	out, err := execElevateHelper(t, deps, "--sign", "req-m", "vault.get", "hash-same", nonce)
	if err != nil {
		t.Fatalf("--sign: %v", err)
	}
	if !strings.Contains(out, `"method":"vault.get"`) {
		t.Fatalf("helper output does not carry the signed method: %s", out)
	}
	var att rpc.Attestation
	if err := json.Unmarshal([]byte(out), &att); err != nil {
		t.Fatal(err)
	}
	trust := rpc.MapTrustStore{att.PubkeyFingerprint: ks.pub}
	wantSignedMethodRefusal(t, rpc.VerifyAttestation(att, trust, ledger, "vault.rotate", "hash-same", clock.Now()))
}

func TestElevateHelperSignRefusesThreeArgumentForm(t *testing.T) {
	ks := &fakeCmdKeystore{}
	if err := ks.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	out, err := execElevateHelper(t, newTestDeps(t, ks, nil), "--sign", "req-1", "hash-1", "nonce-1")
	if !cascade.HasKind(err, cascade.KindInvalidInput) || strings.Contains(out, "sig_b64") {
		t.Fatalf("the pre-method --sign form: out=%q err=%v, want a KindInvalidInput refusal and nothing signed", out, err)
	}
}

func TestSignAttestationBindsMethod(t *testing.T) {
	k := &fakeCmdKeystore{}
	if err := k.GenerateKey(); err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	ledger := rpc.NewNonceLedger(clock)
	nonce, err := ledger.Issue("plugin.update", "hash")
	if err != nil {
		t.Fatal(err)
	}
	att, err := signAttestation(k, clock, "plugin.add", "hash", nonce)
	if err != nil {
		t.Fatal(err)
	}
	if att.Method != "plugin.add" {
		t.Fatalf("Method = %q, want plugin.add", att.Method)
	}
	wantSignedMethodRefusal(t, rpc.VerifyAttestation(att, rpc.MapTrustStore{att.PubkeyFingerprint: k.pub}, ledger, "plugin.update", "hash", clock.Now()))
}
