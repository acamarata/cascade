package install

// Purpose (this file): the Elevator injection seam (fail-closed default,
//   SetElevator/reset) and the plugin.BuiltinHandlers.DispatchIntent
//   bridge in flow.go, which shares this package's Flow-wiring globals.
// SPORT: plugins/cascade-pa/install (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// fakeAttestationVerifier is a local AttestationVerifier double: err
// (nil or non-nil) is returned unconditionally, so a test can drive both
// VerifyElevationWitness branches without a real elevation flow.
type fakeAttestationVerifier struct {
	err   error
	calls int
}

func (f *fakeAttestationVerifier) Verify(context.Context, Attestation) error {
	f.calls++
	return f.err
}

// TestVerifyElevationWitness_SuccessMintsValidWitness is the round-2
// rework proof (T0 decision D2) that the ONLY exported constructor mints a
// valid witness after a real Verify success.
func TestVerifyElevationWitness_SuccessMintsValidWitness(t *testing.T) {
	v := &fakeAttestationVerifier{}
	w, err := VerifyElevationWitness(context.Background(), v, Attestation{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("VerifyElevationWitness: %v", err)
	}
	if !w.Valid() {
		t.Fatal("Valid() = false after a successful verification")
	}
	if v.calls != 1 {
		t.Fatalf("verifier.Verify called %d times, want exactly 1 -- the single verification point", v.calls)
	}
}

// TestVerifyElevationWitness_FailedVerificationYieldsNoWitness proves the
// fail-closed contract: an invalid, replayed or expired attestation (any
// Verify error) must never mint a witness whose Valid() reports true.
func TestVerifyElevationWitness_FailedVerificationYieldsNoWitness(t *testing.T) {
	wantErr := cascade.New(cascade.KindPermissionDenied, "boom: attestation replayed")
	v := &fakeAttestationVerifier{err: wantErr}
	w, err := VerifyElevationWitness(context.Background(), v, Attestation{RequestID: "req-2"})
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("VerifyElevationWitness: err = %v, want it to carry KindPermissionDenied", err)
	}
	if w.Valid() {
		t.Fatal("Valid() = true after a failed verification -- fail-closed contract violated")
	}
}

// TestVerifyElevationWitness_NilVerifierRefuses proves an absent verifier
// is a refusal, never a default approval.
func TestVerifyElevationWitness_NilVerifierRefuses(t *testing.T) {
	w, err := VerifyElevationWitness(context.Background(), nil, Attestation{RequestID: "req-3"})
	if err == nil || w.Valid() {
		t.Fatalf("VerifyElevationWitness(nil verifier) = (%+v, %v), want a fail-closed refusal", w, err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
}

// TestVerifyElevationWitness_EmptyRequestIDRefuses proves a verifier that
// approves an attestation carrying no request id still yields no witness
// -- Valid() must never report true for the zero requestID.
func TestVerifyElevationWitness_EmptyRequestIDRefuses(t *testing.T) {
	w, err := VerifyElevationWitness(context.Background(), &fakeAttestationVerifier{}, Attestation{})
	if err == nil || w.Valid() {
		t.Fatalf("VerifyElevationWitness(empty request id) = (%+v, %v), want a refusal", w, err)
	}
}

func TestUnconfiguredElevator_FailsClosed(t *testing.T) {
	SetElevator(nil)
	result, err := activeElevator().Elevate(context.Background(), ElevationRequest{PluginID: "x"})
	if err == nil || result.Approved {
		t.Fatalf("Elevate() = (%+v, %v), want a fail-closed refusal, never Approved:true", result, err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Elevate() error = %v, want KindUnavailable", err)
	}
}

func TestSetElevator_InjectsAndResets(t *testing.T) {
	fake := &fakeElevator{result: ElevationResult{Approved: true}}
	SetElevator(fake)
	t.Cleanup(func() { SetElevator(nil) })

	result, err := activeElevator().Elevate(context.Background(), ElevationRequest{PluginID: "x"})
	if err != nil || !result.Approved {
		t.Fatalf("Elevate() = (%+v, %v), want the injected fake's approval", result, err)
	}
	if fake.calls != 1 {
		t.Fatalf("fake.calls = %d, want 1", fake.calls)
	}

	SetElevator(nil)
	if _, err := activeElevator().Elevate(context.Background(), ElevationRequest{}); err == nil {
		t.Fatal("Elevate() after SetElevator(nil) = nil error, want the unconfigured default restored")
	}
}

// TestFlowDispatchIntent covers the plugin.BuiltinHandlers.DispatchIntent
// bridge (flow.go): unknown name, an unwired Flow, malformed input, and a
// wired happy-path round trip through the wire-level JSON envelope.
func TestFlowDispatchIntent(t *testing.T) {
	t.Cleanup(func() { SetFlow(nil) })

	if _, err := DispatchIntent(context.Background(), "something.else", nil); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("unknown name error = %v, want KindNotFound", err)
	}
	SetFlow(nil)
	if _, err := DispatchIntent(context.Background(), IntentName, []byte(`{"intent":"git push"}`)); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("unwired error = %v, want KindUnavailable", err)
	}

	f, _ := newDeps(newCandidate("git-tools", plugin.RuntimeBuiltin))
	SetFlow(f)
	if _, err := DispatchIntent(context.Background(), IntentName, []byte(`not json`)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("malformed input error = %v, want KindInvalidInput", err)
	}
	out, err := DispatchIntent(context.Background(), IntentName, []byte(`{"intent":"git push"}`))
	if err != nil {
		t.Fatalf("DispatchIntent() error = %v, want a clean round trip", err)
	}
	var result RunResult
	if err := json.Unmarshal(out, &result); err != nil || !result.Resumed {
		t.Fatalf("DispatchIntent() output = %s (err=%v), want a resumed RunResult", out, err)
	}
}

func TestFlowRunIntent_SnapshotError(t *testing.T) {
	f, _ := newDeps(newCandidate("git-tools", plugin.RuntimeBuiltin))
	f.deps.Snapshot = func(context.Context) (plugin.ManifestSet, *plugin.VerifiedIndex, error) {
		return nil, nil, cascade.New(cascade.KindUnavailable, "snapshot source unreachable")
	}
	if _, err := f.RunIntent(context.Background(), "git push", ""); err == nil {
		t.Fatal("RunIntent() = nil error on a failing Snapshot seam, want it propagated")
	}
}
