package rpc

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileTierRefusesEveryElevatedVerbBeforeHandler(t *testing.T) {
	if elevation.DevkeysBuild() {
		t.Skip("development signing enabled")
	}
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	pub, priv := genAttestTestKey(t)
	if err := os.WriteFile(filepath.Join(dir, "elevation.key"), []byte(base64.StdEncoding.EncodeToString(priv)), 0600); err != nil {
		t.Fatal(err)
	}
	sel := elevation.Selector{DataDir: dir, Sources: []elevation.CustodySource{{Tier: elevation.CustodyFile, Name: "file", Open: func(string) (elevation.ElevationKeystore, bool) {
		_, err := os.Stat(filepath.Join(dir, "elevation.key"))
		return nil, err == nil
	}}}}
	fileGate := func() (string, bool) { c := sel.Select(); return string(c.Tier()), c.Tier().SatisfiesElevation() }
	if len(elevationTable) == 0 {
		t.Fatal("empty elevated table")
	}
	for _, gate := range []CustodyGate{nil, fileGate} {
		for _, rule := range elevationTable {
			for _, signed := range []bool{false, true} {
				assertCustodyDispatch(t, gate, rule.method, signed, pub, priv)
			}
		}
	}
	assertCustodyIssuedAttestation(t, fileGate, pub, priv)
}

func assertCustodyDispatch(t *testing.T, gate CustodyGate, method string, signed bool, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	ledger := NewNonceLedger(clock)
	args := json.RawMessage(`{"process_tier":true,"grant_expand":true,"keep":"local","direction":"loosen","allow_remote":true,"enable":true,"enable_remote_runtime":true}`)
	if !IsElevated(method, args) {
		t.Fatalf("fixture does not elevate %s", method)
	}
	params := args
	if signed {
		att := Attestation{RequestID: "custody-test", ActionHash: hashParams(args), Nonce: "file-signed-nonce", PubkeyFingerprint: "file-fp", IssuedUnix: clock.Now().Unix(), ExpUnix: clock.Now().Add(time.Minute).Unix()}
		sig := ed25519.Sign(priv, signedFields(att))
		att.SigB64 = base64.StdEncoding.EncodeToString(sig)
		if !ed25519.Verify(pub, signedFields(att), sig) {
			t.Fatal("invalid signed fixture")
		}
		var err error
		params, err = json.Marshal(elevatedEnvelope{Attestation: &att, Args: args})
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	reg := NewRegistry()
	reg.Register(method, func(context.Context, json.RawMessage) (any, error) { calls++; return nil, nil })
	reg.Use(ElevationMiddleware(ElevationDeps{Ledger: ledger, Trust: MapTrustStore{"file-fp": pub}, Clock: clock, Custody: gate}))
	_, err := reg.Dispatch(t.Context(), &Request{Method: method, Params: params})
	tier := "file"
	if gate == nil {
		tier = "none"
	}
	if err == nil || err.Code != -32012 {
		t.Fatalf("%s signed=%v: %v", method, signed, err)
	}
	data, ok := err.Data.(map[string]string)
	if !ok || data["reason"] != ReasonCustodyTier || data["tier"] != tier || calls != 0 || ledger.Len() != 0 {
		t.Fatalf("%s: data=%v calls=%d nonces=%d", method, err.Data, calls, ledger.Len())
	}
}
func assertCustodyIssuedAttestation(t *testing.T, gate CustodyGate, pub ed25519.PublicKey, priv ed25519.PrivateKey) {
	t.Helper()
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	ledger := NewNonceLedger(clock)
	args := json.RawMessage("{}")
	nonce, err := ledger.Issue("vault.get", hashParams(args))
	if err != nil {
		t.Fatal(err)
	}
	att := Attestation{RequestID: "custody-issued", ActionHash: hashParams(args), Nonce: nonce, PubkeyFingerprint: "file-fp", IssuedUnix: clock.Now().Unix(), ExpUnix: clock.Now().Add(time.Minute).Unix()}
	att.SigB64 = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signedFields(att)))
	params, err := json.Marshal(elevatedEnvelope{Attestation: &att, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	trust := MapTrustStore{"file-fp": pub}
	calls := 0
	reg := NewRegistry()
	reg.Register("vault.get", func(context.Context, json.RawMessage) (any, error) { calls++; return nil, nil })
	reg.Use(ElevationMiddleware(ElevationDeps{Ledger: ledger, Trust: trust, Clock: clock, Custody: gate}))
	_, refusal := reg.Dispatch(t.Context(), &Request{Method: "vault.get", Params: params})
	if refusal == nil || refusal.Code != -32012 || calls != 0 || ledger.Len() != 1 {
		t.Fatalf("refusal=%v calls=%d ledger=%d", refusal, calls, ledger.Len())
	}
	// The refused request retained its nonce; the real verifier proves the signed input was otherwise valid.
	if err := VerifyAttestation(att, trust, ledger, "vault.get", hashParams(args), clock.Now()); err != nil {
		t.Fatalf("valid attestation fixture: %v", err)
	}
	if ledger.Len() != 0 {
		t.Fatal("verification did not consume the retained nonce")
	}
}
