package rpc

// Purpose (this file): the witness is minted only by the middleware, only
//   after a verified attestation and a recorded grant, and means exactly one
//   verb and one params hash.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type foreignKey struct{}

func TestVerifiedElevationOnlyFromMiddleware(t *testing.T) {
	// A foreign key, even carrying the real type, is not the witness.
	forged := context.WithValue(context.Background(), foreignKey{}, VerifiedElevation{method: "vault.get", paramsHash: "h"})
	if _, ok := VerifiedElevationFrom(forged); ok {
		t.Error("a witness under a foreign context key was accepted")
	}
	if _, ok := VerifiedElevationFrom(context.WithValue(context.Background(), "witness", "vault.get")); ok { //nolint:staticcheck,revive // a string key is exactly the forgery being tried
		t.Error("a string-keyed value was accepted as a witness")
	}
	// The zero value is absent, however it was attached.
	if _, ok := VerifiedElevationFrom(withVerifiedElevation(context.Background(), VerifiedElevation{})); ok {
		t.Error("a zero VerifiedElevation reported present")
	}
	if _, ok := VerifiedElevationFrom(context.Background()); ok {
		t.Error("an empty context reported a witness")
	}

	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`

	assertFailedVerificationMintsNothing(t, args)

	// A success hands the handler a witness for this verb and these args.
	f := newMWFixture(t, "vault.get")
	nonce := f.challenge(t, args)
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err != nil {
		t.Fatal(err)
	}
	w, ok := VerifiedElevationFrom(f.gotCtx)
	if !ok || w.Method() != "vault.get" || w.ParamsHash() != hashParams(json.RawMessage(args)) {
		t.Fatalf("witness = %+v ok=%v, want vault.get over the args hash", w, ok)
	}
	// The witness is attached only after the grant was recorded: neither the
	// attempt nor the grant append saw one.
	if got := strings.Join(f.audit.phases(), ","); got != "attempt,attempt,grant" {
		t.Fatalf("phases after a granted call = %s, want attempt,attempt,grant", got)
	}
	if w := f.audit.witnessedPhases(); len(w) != 0 {
		t.Fatalf("a witness was attached before the grant was recorded: appended under one at %v", w)
	}
	if !w.VerifiedAt().Equal(f.clock.Now()) {
		t.Errorf("VerifiedAt = %v, want the injected clock's %v", w.VerifiedAt(), f.clock.Now())
	}
	if string(f.gotArgs) != args {
		t.Errorf("handler args = %s, want the unwrapped %s", f.gotArgs, args)
	}
}

// assertFailedVerificationMintsNothing: an attestation for other params is
// refused, the handler never runs, and no audit append (the deny record is
// appended under the context verification ran in) saw a witness, so a
// witness attached before VerifyAttestation fails here.
func assertFailedVerificationMintsNothing(t *testing.T, args string) {
	t.Helper()
	f := newMWFixture(t, "vault.get")
	nonce := f.challenge(t, args)
	wrongArgs := f.envelope(t, `{"target":"other"}`, nonce) // signed for other params
	var env elevatedEnvelope
	_ = json.Unmarshal(wrongArgs, &env)
	env.Args = json.RawMessage(args) // attestation no longer covers the args
	tampered, _ := json.Marshal(env)
	if _, err := f.wrapped(context.Background(), tampered); err == nil {
		t.Fatal("an attestation for other params must be refused")
	}
	if f.calls != 0 {
		t.Fatalf("handler ran %d times after a failed verification", f.calls)
	}
	// Ordering: no step before the grant runs with a witness. The deny record
	// is appended under the context verification ran in, so a witness attached
	// before VerifyAttestation shows up here.
	if got := strings.Join(f.audit.phases(), ","); got != "attempt,attempt,deny" {
		t.Fatalf("phases after a failed verification = %s, want attempt,attempt,deny", got)
	}
	if w := f.audit.witnessedPhases(); len(w) != 0 {
		t.Fatalf("a witness existed before verification succeeded: appended under one at %v", w)
	}
}

// A witness already in the incoming context never reaches another dispatch:
// the middleware strips it from every call it does not verify itself.
func TestWitnessDoesNotRideIntoAnotherDispatch(t *testing.T) {
	f := newMWFixture(t, "status.get")
	rode := withVerifiedElevation(context.Background(), newVerifiedElevation(f.issuer, "vault.get", "h", time.Unix(1, 0)))
	if _, err := f.wrapped(rode, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if w, ok := VerifiedElevationFrom(f.gotCtx); ok {
		t.Errorf("a witness for another verb reached the handler: %+v", w)
	}
}

func TestParamsHashMatchesMiddleware(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")
	nonce := f.challenge(t, args)
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err != nil {
		t.Fatal(err)
	}
	w, _ := VerifiedElevationFrom(f.gotCtx)
	if got := ParamsHash(json.RawMessage(args)); got != w.ParamsHash() || got != hashParams(json.RawMessage(args)) {
		t.Errorf("ParamsHash = %s, witness = %s, hashParams = %s", got, w.ParamsHash(), hashParams(json.RawMessage(args)))
	}
	if ParamsHash(json.RawMessage(`{"target": "db-password"}`)) == w.ParamsHash() { // same value, one space more
		t.Error("ParamsHash treated two encodings of one value as equal; it must be byte-exact")
	}
	grant := f.audit.recs[len(f.audit.recs)-1]
	if grant.Phase != ElevationPhaseGrant || grant.ParamsHash != w.ParamsHash() {
		t.Errorf("grant record %+v does not carry the witness hash %s", grant, w.ParamsHash())
	}
}

// A witness remembers the issuer that minted it: an attestor bound to another
// issuer (or to none) never accepts it.
func TestWitnessCarriesItsIssuer(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, f.challenge(t, args))); err != nil {
		t.Fatal(err)
	}
	w, ok := VerifiedElevationFrom(f.gotCtx)
	if !ok {
		t.Fatal("no witness")
	}
	if !w.IssuedBy(f.issuer.Verifier()) {
		t.Error("the witness is not issued by the middleware's own issuer")
	}
	if w.IssuedBy(NewWitnessIssuer().Verifier()) || w.IssuedBy(WitnessVerifier{}) {
		t.Error("the witness was accepted for a different or nil issuer")
	}
	// A witness with no issuer cannot exist in a context: it reads absent.
	if _, ok := VerifiedElevationFrom(withVerifiedElevation(context.Background(), newVerifiedElevation(nil, "vault.get", "h", time.Unix(1, 0)))); ok {
		t.Error("an issuer-less witness reported present")
	}
}

// A gate built without an Issuer (a ceremony gate with its own trust store)
// still verifies and runs the handler, but mints no witness.
func TestGateWithoutIssuerMintsNoWitness(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixtureWith(t, "vault.get", nil)
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, f.challenge(t, args))); err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("handler ran %d times, want 1", f.calls)
	}
	if w, ok := VerifiedElevationFrom(f.gotCtx); ok {
		t.Errorf("a gate without an issuer minted a witness: %+v", w)
	}
	if f.gotCtx.Value(witnessKey{}) != nil {
		t.Error("a gate without an issuer attached a witness value to the handler context")
	}
}
