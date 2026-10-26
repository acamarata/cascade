package rpc

// Purpose (this file): the shared fixture of the elevation middleware tests
//   (a recording, failable auditor; a signed-envelope builder) and the audit
//   ordering tests: attempt, then grant or deny, never params, and a refusal
//   whenever a record cannot be written.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/runtime"
)

// recordingAuditor keeps every record it was handed, notes each append whose
// context already carried a witness, and fails the phases it was told to fail.
type recordingAuditor struct {
	mu        sync.Mutex
	recs      []ElevationAuditRecord
	witnessed []string
	fails     map[string]bool
}

func (a *recordingAuditor) AppendElevation(ctx context.Context, rec ElevationAuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := VerifiedElevationFrom(ctx); ok {
		a.witnessed = append(a.witnessed, rec.Phase)
	}
	if a.fails[rec.Phase] {
		return errors.New("audit store down")
	}
	a.recs = append(a.recs, rec)
	return nil
}

// witnessedPhases lists the phases appended under a context that already held
// a witness. The middleware mints one only after the grant was recorded, so
// any entry means the witness was attached too early.
func (a *recordingAuditor) witnessedPhases() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.witnessed...)
}

// fixtureKeystore is the fixtures' custody keystore: it signs with the
// fixture's own key, so the custody key is the trusted key.
type fixtureKeystore struct{ priv ed25519.PrivateKey }

func (fixtureKeystore) GenerateKey() error { return nil }
func (k fixtureKeystore) PubKeyB64() (string, error) {
	return base64.StdEncoding.EncodeToString(k.priv.Public().(ed25519.PublicKey)), nil
}
func (k fixtureKeystore) Sign(p []byte) ([]byte, error) { return ed25519.Sign(k.priv, p), nil }
func (fixtureKeystore) IsAvailable() bool               { return true }
func (fixtureKeystore) Tier() elevation.StorageTier     { return elevation.TierOSKeychain }

// fixtureCustody selects custody the way the daemon does, through an
// elevation.Selector over one platform-tier source, with HOME, USERPROFILE
// and CASCADE_HOME pointed at a fresh temp dir, so no host keystore is read.
func fixtureCustody(t *testing.T, priv ed25519.PrivateKey) CustodyGate {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, dir)
	}
	sel := elevation.Selector{DataDir: dir, Sources: []elevation.CustodySource{{
		Tier: elevation.CustodyPlatform, Name: "fixture",
		Open: func(string) (elevation.ElevationKeystore, bool) { return fixtureKeystore{priv: priv}, true },
	}}}
	if tier := sel.Select().Tier(); !tier.SatisfiesElevation() {
		t.Fatalf("fixture custody selected tier %q, want one that satisfies elevation", tier)
	}
	return func() (string, bool) { c := sel.Select(); return string(c.Tier()), c.Tier().SatisfiesElevation() }
}

func (a *recordingAuditor) phases() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.recs))
	for _, r := range a.recs {
		out = append(out, r.Phase)
	}
	return out
}

// mwFixture is a middleware over one trusted key, a recording auditor and a
// handler that counts its runs and keeps the context it ran with.
type mwFixture struct {
	clock   *runtime.FixedClock
	priv    ed25519.PrivateKey
	ledger  *NonceLedger
	issuer  *WitnessIssuer
	audit   *recordingAuditor
	calls   int
	gotCtx  context.Context
	gotArgs json.RawMessage
	wrapped HandlerFunc
}

// newMWFixture wraps a counting handler for method with the real middleware.
func newMWFixture(t *testing.T, method string) *mwFixture {
	t.Helper()
	return newMWFixtureWith(t, method, NewWitnessIssuer())
}

// newMWFixtureWith is newMWFixture with an explicit issuer (nil: no witnesses).
func newMWFixtureWith(t *testing.T, method string, issuer *WitnessIssuer) *mwFixture {
	t.Helper()
	pub, priv := genAttestTestKey(t)
	f := &mwFixture{clock: runtime.NewFixedClock(time.Unix(4000, 0)), priv: priv, audit: &recordingAuditor{fails: map[string]bool{}}}
	f.ledger = NewNonceLedger(f.clock)
	f.issuer = issuer
	mw := ElevationMiddleware(ElevationDeps{
		Ledger: f.ledger, Trust: MapTrustStore{"fp1": pub}, Clock: f.clock, Audit: f.audit, Issuer: f.issuer,
		Custody: fixtureCustody(t, priv),
	})
	f.wrapped = mw(method, func(ctx context.Context, params json.RawMessage) (any, error) {
		f.calls++
		f.gotCtx, f.gotArgs = ctx, params
		return "ran", nil
	})
	return f
}

// challenge sends params unattested and returns the nonce it was given.
func (f *mwFixture) challenge(t *testing.T, params string) string {
	t.Helper()
	_, err := f.wrapped(context.Background(), json.RawMessage(params))
	eo, ok := err.(*ErrorObject)
	if !ok || eo.Code != codeElevationRequired {
		t.Fatalf("unattested call: err = %#v, want ELEVATION_REQUIRED", err)
	}
	return eo.Data.(elevationRequiredData).Nonce
}

// envelope signs args under nonce and wraps both the way the client does.
func (f *mwFixture) envelope(t *testing.T, args, nonce string) json.RawMessage {
	t.Helper()
	att := Attestation{
		RequestID: "req", ActionHash: hashParams(json.RawMessage(args)), Nonce: nonce, PubkeyFingerprint: "fp1",
		IssuedUnix: f.clock.Now().Unix(), ExpUnix: f.clock.Now().Add(time.Minute).Unix(),
	}
	att.SigB64 = base64.StdEncoding.EncodeToString(ed25519.Sign(f.priv, signedFields(att)))
	b, err := json.Marshal(elevatedEnvelope{Attestation: &att, Args: json.RawMessage(args)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func skipIfPlatformRefuses(t *testing.T) {
	t.Helper()
	if platformElevationRefusal() != nil {
		t.Skip("Windows tier-2 refuses before the attestation flow runs")
	}
}

func TestElevationMiddlewareAuditsGrantAndDeny(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`
	f := newMWFixture(t, "vault.get")

	nonce := f.challenge(t, args)
	if _, err := f.wrapped(context.Background(), f.envelope(t, args, nonce)); err != nil {
		t.Fatalf("attested call: %v", err)
	}
	// attempt (challenge), attempt (attested), grant.
	if got := strings.Join(f.audit.phases(), ","); got != "attempt,attempt,grant" {
		t.Fatalf("phases after a granted call = %s, want attempt,attempt,grant", got)
	}
	for _, rec := range f.audit.recs {
		if rec.Method != "vault.get" || rec.ParamsHash != hashParams(json.RawMessage(args)) {
			t.Errorf("record %+v does not carry the method and the params hash", rec)
		}
		if strings.Contains(rec.Reason+rec.Method+rec.ParamsHash, "db-password") {
			t.Errorf("record %+v leaks the params", rec)
		}
	}

	// A signature that does not verify: attempt, then deny with a reason code.
	f2 := newMWFixture(t, "vault.get")
	nonce2 := f2.challenge(t, args)
	bad := f2.envelope(t, args, nonce2)
	bad = json.RawMessage(strings.Replace(string(bad), `"sig_b64":"`, `"sig_b64":"AAAA`, 1))
	if _, err := f2.wrapped(context.Background(), bad); err == nil {
		t.Fatal("a corrupted signature must be refused")
	}
	if got := strings.Join(f2.audit.phases(), ","); got != "attempt,attempt,deny" {
		t.Fatalf("phases after a denied call = %s, want attempt,attempt,deny", got)
	}
	if deny := f2.audit.recs[2]; deny.Reason == "" || deny.Reason == args {
		t.Errorf("deny record reason = %q, want a short constant code", deny.Reason)
	}
	if f2.calls != 0 {
		t.Errorf("handler ran %d times after a denial", f2.calls)
	}
}

func TestElevationMiddlewareAuditFailureRefuses(t *testing.T) {
	skipIfPlatformRefuses(t)
	const args = `{"target":"db-password"}`

	// A failing attempt append on the challenge path withdraws the nonce.
	f := newMWFixture(t, "vault.get")
	f.audit.fails[ElevationPhaseAttempt] = true
	_, err := f.wrapped(context.Background(), json.RawMessage(args))
	assertRefusedUnavailable(t, err)
	if f.ledger.Len() != 0 {
		t.Errorf("ledger holds %d nonces after a refused challenge, want 0", f.ledger.Len())
	}

	// A failing attempt append on the attested path: nothing runs, the nonce survives.
	f = newMWFixture(t, "vault.get")
	nonce := f.challenge(t, args)
	f.audit.fails[ElevationPhaseAttempt] = true
	_, err = f.wrapped(context.Background(), f.envelope(t, args, nonce))
	assertRefusedUnavailable(t, err)
	if f.calls != 0 {
		t.Errorf("handler ran %d times after a failed attempt append", f.calls)
	}
	if f.ledger.Len() != 1 {
		t.Errorf("ledger holds %d nonces after a failed attempt append, want the nonce to survive", f.ledger.Len())
	}

	// A failing GRANT append: a valid attestation still must not run the handler.
	f = newMWFixture(t, "vault.get")
	nonce = f.challenge(t, args)
	f.audit.fails[ElevationPhaseGrant] = true
	_, err = f.wrapped(context.Background(), f.envelope(t, args, nonce))
	assertRefusedUnavailable(t, err)
	if f.calls != 0 {
		t.Errorf("handler ran %d times after a failed grant append", f.calls)
	}

	// No auditor at all: every elevated verb refuses; a plain verb does not.
	mw := ElevationMiddleware(ElevationDeps{
		Ledger: NewNonceLedger(f.clock), Trust: MapTrustStore{}, Clock: f.clock,
		Custody: fixtureCustody(t, f.priv),
	})
	ran := 0
	count := func(context.Context, json.RawMessage) (any, error) { ran++; return nil, nil }
	if _, err := mw("vault.get", count)(context.Background(), json.RawMessage(args)); err == nil || ran != 0 {
		t.Errorf("nil auditor: err=%v ran=%d, want a refusal and no run", err, ran)
	}
	if _, err := mw("status.get", count)(context.Background(), nil); err != nil || ran != 1 {
		t.Errorf("nil auditor must not affect a plain verb: err=%v ran=%d", err, ran)
	}
}

func assertRefusedUnavailable(t *testing.T, err error) {
	t.Helper()
	eo, ok := err.(*ErrorObject)
	if !ok {
		t.Fatalf("err = %#v, want an ErrorObject refusal", err)
	}
	if eo.Code == codeElevationRequired {
		t.Fatalf("an audit failure must not hand out a challenge: %+v", eo)
	}
	if !strings.Contains(eo.Message, "audit record could not be written") {
		t.Fatalf("refusal message = %q, want the audit-failure refusal", eo.Message)
	}
}
