// Purpose: cmd-level tests for `cascade elevate-helper`. The most
//
//	important test here is TestElevateHelperCmd_SignVerifiesAgainstReal
//	Verifier: it signs with THIS command's own code path and verifies
//	with internal/rpc.VerifyAttestation, the REAL, already-shipped
//	verifier — never a reimplementation (Art.2).
//
// SPORT: cmd/cascade elevate-helper tests/ADDED (P1-E04-W1-S07-T6).
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeCmdKeystore is an in-process ElevationKeystore fake for these
// command-level tests (Art.1: _test.go only). It mirrors keystore_test.go's
// fake in internal/elevation but is redeclared locally because that one is
// unexported to its own package.
type fakeCmdKeystore struct {
	pub         ed25519.PublicKey
	priv        ed25519.PrivateKey
	authFails   bool
	notEnrolled bool
}

func (f *fakeCmdKeystore) IsAvailable() bool           { return true }
func (f *fakeCmdKeystore) Tier() elevation.StorageTier { return elevation.TierOSKeychain }

func (f *fakeCmdKeystore) GenerateKey() error {
	if f.pub != nil {
		return nil
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	f.pub, f.priv = pub, priv
	return nil
}

func (f *fakeCmdKeystore) PubKeyB64() (string, error) {
	if f.notEnrolled || f.pub == nil {
		return "", elevation.ErrHelperNotEnrolled()
	}
	return base64.StdEncoding.EncodeToString(f.pub), nil
}

func (f *fakeCmdKeystore) Sign(payload []byte) ([]byte, error) {
	if f.authFails {
		return nil, elevation.ErrAuthFailed(nil)
	}
	if f.priv == nil {
		return nil, elevation.ErrHelperNotEnrolled()
	}
	return ed25519.Sign(f.priv, payload), nil
}

func newTestDeps(t *testing.T, ks elevation.ElevationKeystore, getenv runtime.Getenv) elevateHelperDeps {
	t.Helper()
	backend := elevation.NewFileBackend(t.TempDir())
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	return elevateHelperDeps{
		Keystore:     func() elevation.ElevationKeystore { return ks },
		TrustBackend: func() elevation.Backend { return backend },
		Clock:        runtime.NewFixedClock(time.Unix(1_700_000_000, 0)),
		Getenv:       getenv,
	}
}

func execElevateHelper(t *testing.T, deps elevateHelperDeps, args ...string) (string, error) {
	t.Helper()
	cmd := newElevateHelperCmd(deps)
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestElevateHelperCmd_Enroll_FirstRun(t *testing.T) {
	deps := newTestDeps(t, &fakeCmdKeystore{}, nil)
	out, err := execElevateHelper(t, deps, "--enroll")
	if err != nil {
		t.Fatalf("--enroll: %v", err)
	}
	if !bytes.Contains([]byte(out), []byte("fingerprint sha256:")) {
		t.Errorf("--enroll stderr output missing fingerprint line, got: %q", out)
	}
}

func TestElevateHelperCmd_Enroll_SecondRun_Refused(t *testing.T) {
	ks := &fakeCmdKeystore{}
	deps := newTestDeps(t, ks, nil)
	if _, err := execElevateHelper(t, deps, "--enroll"); err != nil {
		t.Fatalf("first --enroll: %v", err)
	}
	_, err := execElevateHelper(t, deps, "--enroll")
	if err == nil {
		t.Fatal("second --enroll must be refused (TOFU)")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Errorf("kind = %v (ok=%v), want KindConflict", kind, ok)
	}
}

func TestElevateHelperCmd_Sign_MissingEnrollment(t *testing.T) {
	deps := newTestDeps(t, &fakeCmdKeystore{notEnrolled: true}, nil)
	_, err := execElevateHelper(t, deps, "--sign", "req-1", "hash-1", "nonce-1")
	if err == nil {
		t.Fatal("--sign with no enrolled key must fail")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindNotFound {
		t.Errorf("kind = %v (ok=%v), want KindNotFound", kind, ok)
	}
}

func TestElevateHelperCmd_Sign_AuthFailure(t *testing.T) {
	ks := &fakeCmdKeystore{}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ks.authFails = true
	deps := newTestDeps(t, ks, nil)
	_, err := execElevateHelper(t, deps, "--sign", "req-1", "hash-1", "nonce-1")
	if err == nil {
		t.Fatal("--sign must fail when local authentication fails")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Errorf("kind = %v (ok=%v), want KindPermissionDenied", kind, ok)
	}
}

// TestElevateHelperCmd_NoInput_FailsFastBeforeAuth proves CASCADE_NO_INPUT=1
// refuses BEFORE any auth prompt: ks.authFails is left false and a
// generated key IS present, so if this reached Sign at all it would
// succeed — the test only passes if the CASCADE_NO_INPUT guard short-
// circuits ahead of that call.
func TestElevateHelperCmd_NoInput_FailsFastBeforeAuth(t *testing.T) {
	ks := &fakeCmdKeystore{}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	deps := newTestDeps(t, ks, func(key string) string {
		if key == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	})
	_, err := execElevateHelper(t, deps, "--sign", "req-1", "hash-1", "nonce-1")
	if err == nil {
		t.Fatal("CASCADE_NO_INPUT=1 must make --sign fail fast, not succeed")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Errorf("kind = %v (ok=%v), want KindPermissionDenied", kind, ok)
	}
}

func TestElevateHelperCmd_BothFlags_Refused(t *testing.T) {
	deps := newTestDeps(t, &fakeCmdKeystore{}, nil)
	_, err := execElevateHelper(t, deps, "--enroll", "--sign", "a", "b", "c")
	if err == nil {
		t.Fatal("--enroll and --sign together must be refused")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

func TestElevateHelperCmd_NeitherFlag_Refused(t *testing.T) {
	deps := newTestDeps(t, &fakeCmdKeystore{}, nil)
	_, err := execElevateHelper(t, deps)
	if err == nil {
		t.Fatal("neither --enroll nor --sign must be refused")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

func TestElevateHelperCmd_Sign_WrongArgCount(t *testing.T) {
	deps := newTestDeps(t, &fakeCmdKeystore{}, nil)
	_, err := execElevateHelper(t, deps, "--sign", "only-one-arg")
	if err == nil {
		t.Fatal("--sign with the wrong argument count must be refused")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
		t.Errorf("kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

// TestElevateHelperCmd_SignVerifiesAgainstRealVerifier is this ticket's
// Art.2 real-counterpart proof: it signs an attestation through THIS
// command's actual code path (runElevateHelperSign, canonicalSignedBytes)
// and verifies the result with internal/rpc.VerifyAttestation — the real,
// already-shipped verifier this ticket must match, not a reimplementation
// of it.
func TestElevateHelperCmd_SignVerifiesAgainstRealVerifier(t *testing.T) {
	att, trust, ledger, clock := signRealAttestation(t, "req-42", "hash-abc")

	if att.ExpUnix-att.IssuedUnix != 300 {
		t.Errorf("exp - issued = %d, want 300 (5 minutes)", att.ExpUnix-att.IssuedUnix)
	}
	if err := rpc.VerifyAttestation(att, trust, ledger, "vault.get", "hash-abc", clock.Now()); err != nil {
		t.Fatalf("REAL verifier (internal/rpc.VerifyAttestation) rejected this helper's attestation: %v", err)
	}
}

// TestElevateHelperCmd_SignVerifiesAgainstRealVerifier_RejectsTampering
// proves the round trip checks the signature, not merely the JSON shape:
// a field flipped after signing must still be rejected by the REAL
// verifier.
func TestElevateHelperCmd_SignVerifiesAgainstRealVerifier_RejectsTampering(t *testing.T) {
	att, trust, ledger, clock := signRealAttestation(t, "req-43", "hash-def")

	tampered := att
	tampered.ActionHash = "tampered"
	tamperErr := rpc.VerifyAttestation(tampered, trust, ledger, "vault.get", "hash-def", clock.Now())
	if tamperErr == nil {
		t.Fatal("real verifier accepted a tampered attestation")
	}
	if kind, ok := cascade.KindOf(tamperErr); !ok || kind != cascade.KindIntegrity {
		t.Errorf("kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

// signRealAttestation drives the elevate-helper --sign path end to end for
// method "vault.get"/paramsHash actionHash: it issues a real nonce from
// internal/rpc's own NonceLedger, signs through THIS command's production
// code path, and returns the resulting attestation plus a TrustStore/
// ledger/clock triple ready for internal/rpc.VerifyAttestation — the real,
// unmodified verifier — to check it against.
func signRealAttestation(t *testing.T, requestID, actionHash string) (rpc.Attestation, rpc.MapTrustStore, *rpc.NonceLedger, *runtime.FixedClock) {
	t.Helper()
	ks := &fakeCmdKeystore{}
	if err := ks.GenerateKey(); err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	deps := newTestDeps(t, ks, nil)
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	deps.Clock = clock

	trust := rpc.MapTrustStore{}
	ledger := rpc.NewNonceLedger(clock)
	nonce, err := ledger.Issue("vault.get", actionHash)
	if err != nil {
		t.Fatalf("ledger.Issue: %v", err)
	}

	out, err := execElevateHelper(t, deps, "--sign", requestID, actionHash, nonce)
	if err != nil {
		t.Fatalf("--sign: %v", err)
	}
	var att rpc.Attestation
	if err := json.Unmarshal([]byte(out), &att); err != nil {
		t.Fatalf("unmarshal attestation: %v\noutput: %q", err, out)
	}
	trust[att.PubkeyFingerprint] = ks.pub
	return att, trust, ledger, clock
}

// TestProductionElevateHelperDeps_ConstructsRealBackends drives
// productionElevateHelperDeps' real Keystore and TrustBackend closures:
// both only construct values (elevation.NewKeystore wires the real
// cgoBridge but calls none of its methods; elevation.NewFileBackend just
// joins a path) so this is safe without touching the real Keychain or
// triggering an auth prompt.
func TestProductionElevateHelperDeps_ConstructsRealBackends(t *testing.T) {
	deps := productionElevateHelperDeps()
	if deps.Keystore == nil || deps.TrustBackend == nil || deps.Clock == nil || deps.Getenv == nil {
		t.Fatal("productionElevateHelperDeps left a field nil")
	}
	if ks := deps.Keystore(); ks == nil {
		t.Fatal("Keystore() returned nil")
	}
	if tb := deps.TrustBackend(); tb == nil {
		t.Fatal("TrustBackend() returned nil")
	}
}
