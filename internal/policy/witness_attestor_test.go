package policy

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// attestedDispatch drives one attested dispatch of method through the real
// elevation middleware and runs inside in the handler's context: the only
// place a witness exists, and where production reads it. A policy test cannot
// mint a witness itself (nothing outside internal/rpc can), which is the point.
func attestedDispatch(t *testing.T, issuer *rpc.WitnessIssuer, method string, inside func(ctx context.Context)) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	mw := rpc.ElevationMiddleware(rpc.ElevationDeps{
		Ledger: rpc.NewNonceLedger(clock), Trust: rpc.MapTrustStore{"fp": pub}, Clock: clock,
		Audit: rpc.LocalCeremonyAudit{}, Custody: platformCustody(t, priv), Issuer: issuer,
	})
	ran := false
	wrapped := mw(method, func(ctx context.Context, _ json.RawMessage) (any, error) {
		ran = true
		inside(ctx)
		return nil, nil
	})
	args := json.RawMessage(`{"k":"v"}`)
	_, challenge := wrapped(context.Background(), args)
	eo, ok := challenge.(*rpc.ErrorObject)
	if !ok || eo.Code != cascade.RPCCodeElevationRequired {
		if goruntime.GOOS == "windows" {
			t.Skipf("no challenge issued (%v): Windows tier-2 refuses before the attestation flow", challenge)
		}
		t.Fatalf("unattested dispatch of %s = %v, want ELEVATION_REQUIRED with a nonce", method, challenge)
	}
	data, _ := json.Marshal(eo.Data)
	var nonce struct{ Nonce string }
	_ = json.Unmarshal(data, &nonce)
	signed, _ := json.Marshal(struct { // the documented signed fields, alphabetical
		ActionHash string `json:"action_hash"`
		ExpUnix    int64  `json:"exp_unix"`
		IssuedUnix int64  `json:"issued_unix"`
		Nonce      string `json:"nonce"`
		Pubkey     string `json:"pubkey_fingerprint"`
		RequestID  string `json:"request_id"`
	}{rpc.ParamsHash(args), clock.Now().Add(time.Minute).Unix(), clock.Now().Unix(), nonce.Nonce, "fp", "req"})
	att := rpc.Attestation{
		RequestID: "req", ActionHash: rpc.ParamsHash(args), Nonce: nonce.Nonce, PubkeyFingerprint: "fp",
		IssuedUnix: clock.Now().Unix(), ExpUnix: clock.Now().Add(time.Minute).Unix(),
		SigB64: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, signed)),
	}
	env, _ := json.Marshal(map[string]any{"_attestation": att, "_args": args})
	if _, err := wrapped(context.Background(), env); err != nil {
		t.Fatalf("attested dispatch of %s: %v", method, err)
	}
	if !ran {
		t.Fatal("handler did not run")
	}
}

func TestWitnessAttestorRequiresMatchingMethod(t *testing.T) {
	issuer := rpc.NewWitnessIssuer()
	att := WitnessAttestor(issuer.Verifier())

	// No witness at all: refused with KindElevationRequired.
	err := att.FreshLocalAttestation(context.Background(), "vault.get")
	if kind, ok := cascade.KindOf(err); err == nil || !ok || kind != cascade.KindElevationRequired {
		t.Fatalf("no witness: err = %v, want KindElevationRequired", err)
	}

	got := map[string]error{}
	attestedDispatch(t, issuer, "vault.get", func(ctx context.Context) {
		got["same"] = att.FreshLocalAttestation(ctx, "vault.get")
		got["other"] = att.FreshLocalAttestation(ctx, "vault.rotate")
		got["unelevated"] = att.FreshLocalAttestation(ctx, "status.get")
	})
	if got["same"] != nil {
		t.Errorf("a witness for vault.get did not attest vault.get: %v", got["same"])
	}
	for _, other := range []string{"other", "unelevated"} {
		if kind, ok := cascade.KindOf(got[other]); got[other] == nil || !ok || kind != cascade.KindElevationRequired {
			t.Errorf("a witness for vault.get attested %q: err = %v", other, got[other])
		}
	}

	// After the dispatch returns, nothing keeps the attestation alive.
	if err := att.FreshLocalAttestation(context.Background(), "vault.get"); err == nil {
		t.Error("a fresh context attested vault.get")
	}
}

// A witness minted by another middleware (its own trust store, its own
// issuer) never satisfies the attestor bound to the daemon's issuer, and an
// attestor with no issuer refuses everything.
func TestWitnessAttestorRefusesAnotherIssuersWitness(t *testing.T) {
	mine, theirs := rpc.NewWitnessIssuer(), rpc.NewWitnessIssuer()
	var viaTheirs, viaNil, viaOwn error
	attestedDispatch(t, theirs, "vault.get", func(ctx context.Context) {
		viaTheirs = WitnessAttestor(mine.Verifier()).FreshLocalAttestation(ctx, "vault.get")
		viaNil = WitnessAttestor(rpc.WitnessVerifier{}).FreshLocalAttestation(ctx, "vault.get")
		viaOwn = WitnessAttestor(theirs.Verifier()).FreshLocalAttestation(ctx, "vault.get")
	})
	for name, err := range map[string]error{"another issuer": viaTheirs, "nil issuer": viaNil} {
		if kind, ok := cascade.KindOf(err); err == nil || !ok || kind != cascade.KindElevationRequired {
			t.Errorf("%s: err = %v, want KindElevationRequired", name, err)
		}
	}
	if viaOwn != nil {
		t.Errorf("the issuer's own witness was refused: %v", viaOwn)
	}
}

// custodyKeystore signs with the test's own key, so the custody key is the
// trusted key.
type custodyKeystore struct{ priv ed25519.PrivateKey }

func (custodyKeystore) GenerateKey() error { return nil }
func (k custodyKeystore) PubKeyB64() (string, error) {
	return base64.StdEncoding.EncodeToString(k.priv.Public().(ed25519.PublicKey)), nil
}
func (k custodyKeystore) Sign(p []byte) ([]byte, error) { return ed25519.Sign(k.priv, p), nil }
func (custodyKeystore) IsAvailable() bool               { return true }
func (custodyKeystore) Tier() elevation.StorageTier     { return elevation.TierOSKeychain }

// platformCustody selects custody through an elevation.Selector over one
// platform-tier source, with HOME, USERPROFILE and CASCADE_HOME pointed at a
// fresh temp dir, so no host keystore is read.
func platformCustody(t *testing.T, priv ed25519.PrivateKey) rpc.CustodyGate {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	sel := elevation.Selector{DataDir: dir, Sources: []elevation.CustodySource{{
		Tier: elevation.CustodyPlatform, Name: "test",
		Open: func(string) (elevation.ElevationKeystore, bool) { return custodyKeystore{priv: priv}, true },
	}}}
	if tier := sel.Select().Tier(); !tier.SatisfiesElevation() {
		t.Fatalf("test custody selected tier %q, want one that satisfies elevation", tier)
	}
	return func() (string, bool) { c := sel.Select(); return string(c.Tier()), c.Tier().SatisfiesElevation() }
}
