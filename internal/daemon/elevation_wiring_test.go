//go:build !windows

package daemon

// Purpose (this file): the daemon's elevation middleware over injected
//   custody: the bound path (challenge, attest, witness, audit rows), every
//   refusal that must write nothing (no enrolment, a record for another key,
//   a tier that cannot elevate, no audit log), and the refuse-all variant.
//   No host keystore is touched: custody is an in-memory key.

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// memKeystore is an in-memory ElevationKeystore with a fixed key.
type memKeystore struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newMemKeystore(t *testing.T) *memKeystore {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &memKeystore{pub: pub, priv: priv}
}

func (k *memKeystore) GenerateKey() error { return nil }
func (k *memKeystore) PubKeyB64() (string, error) {
	return base64.StdEncoding.EncodeToString(k.pub), nil
}
func (k *memKeystore) Sign(p []byte) ([]byte, error) { return ed25519.Sign(k.priv, p), nil }
func (k *memKeystore) IsAvailable() bool             { return true }
func (k *memKeystore) Tier() elevation.StorageTier   { return elevation.TierFile }

// selectorOver builds a one-source selector at tier over ks.
func selectorOver(dataDir string, tier elevation.CustodyTier, ks elevation.ElevationKeystore) elevation.Selector {
	return elevation.Selector{DataDir: dataDir, Sources: []elevation.CustodySource{{
		Tier: tier, Name: "test", Open: func(string) (elevation.ElevationKeystore, bool) { return ks, true },
	}}}
}

type elevFixture struct {
	dir   string
	clock *runtime.FixedClock
	log   *audit.Log
	ks    *memKeystore
}

func newElevFixture(t *testing.T) *elevFixture {
	t.Helper()
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	return &elevFixture{
		dir: t.TempDir(), clock: clock, ks: newMemKeystore(t),
		log: audit.New(storetest.NewMemStore(), clock, nil),
	}
}

func (f *elevFixture) enroll(t *testing.T, ks *memKeystore) {
	t.Helper()
	pub, _ := ks.PubKeyB64()
	if _, err := elevation.NewElevationTrustStore(elevation.NewFileBackend(f.dir), f.clock).Enroll(pub); err != nil {
		t.Fatal(err)
	}
}

// handler counts runs and reports the witness it saw.
type elevCounter struct {
	runs    int
	witness rpc.VerifiedElevation
	seen    bool
}

func (c *elevCounter) handler(ctx context.Context, _ json.RawMessage) (any, error) {
	c.runs++
	c.witness, c.seen = rpc.VerifiedElevationFrom(ctx)
	return "ran", nil
}

func (f *elevFixture) kinds(t *testing.T) []string {
	t.Helper()
	page, err := f.log.Query(context.Background(), audit.Filter{Kinds: []audit.Kind{audit.KindElevationAttempt, audit.KindElevationGrant, audit.KindElevationDeny}, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(page.Records))
	for _, r := range page.Records {
		out = append(out, string(r.Kind))
	}
	return out
}

// attestedEnvelope answers a challenge the way the CLI client does.
func (f *elevFixture) attestedEnvelope(t *testing.T, ks *memKeystore, args, nonce string) json.RawMessage {
	t.Helper()
	pub, _ := ks.PubKeyB64()
	fp, _ := elevation.Fingerprint(pub)
	hash := rpc.ParamsHash(json.RawMessage(args))
	signed, _ := json.Marshal(struct {
		ActionHash string `json:"action_hash"`
		ExpUnix    int64  `json:"exp_unix"`
		IssuedUnix int64  `json:"issued_unix"`
		Nonce      string `json:"nonce"`
		Pubkey     string `json:"pubkey_fingerprint"`
		RequestID  string `json:"request_id"`
	}{hash, f.clock.Now().Add(time.Minute).Unix(), f.clock.Now().Unix(), nonce, fp, "req"})
	sig, _ := ks.Sign(signed)
	att := rpc.Attestation{RequestID: "req", ActionHash: hash, Nonce: nonce, PubkeyFingerprint: fp,
		IssuedUnix: f.clock.Now().Unix(), ExpUnix: f.clock.Now().Add(time.Minute).Unix(), SigB64: base64.StdEncoding.EncodeToString(sig)}
	env, _ := json.Marshal(map[string]any{"_attestation": att, "_args": json.RawMessage(args)})
	return env
}

func challengeNonce(t *testing.T, err error) string {
	t.Helper()
	eo, ok := err.(*rpc.ErrorObject)
	if !ok {
		t.Fatalf("err = %#v, want an ErrorObject", err)
	}
	data, _ := json.Marshal(eo.Data)
	var d struct{ Nonce string }
	_ = json.Unmarshal(data, &d)
	if d.Nonce == "" {
		t.Fatalf("no nonce in %+v", eo)
	}
	return d.Nonce
}

func TestNewElevationMiddlewareBoundRoundTrip(t *testing.T) {
	f := newElevFixture(t)
	f.enroll(t, f.ks)
	mw, status := NewElevationMiddleware(f.dir, selectorOver(f.dir, elevation.CustodyPlatform, f.ks), f.log, f.clock)
	if !status.Bound || status.Tier != "platform" || status.Reason != "" {
		t.Fatalf("status = %+v, want bound at tier platform", status)
	}
	c := &elevCounter{}
	wrapped := mw("plugin.add", c.handler)
	const args = `{"manifest":"x"}`
	_, err := wrapped(context.Background(), json.RawMessage(args))
	nonce := challengeNonce(t, err)
	if c.runs != 0 {
		t.Fatal("handler ran before attestation")
	}
	if _, err := wrapped(context.Background(), f.attestedEnvelope(t, f.ks, args, nonce)); err != nil {
		t.Fatalf("attested call: %v", err)
	}
	if c.runs != 1 || !c.seen || c.witness.Method() != "plugin.add" || c.witness.ParamsHash() != rpc.ParamsHash(json.RawMessage(args)) {
		t.Fatalf("runs=%d witness=%+v seen=%v", c.runs, c.witness, c.seen)
	}
	if status.Verifier == (rpc.WitnessVerifier{}) || !c.witness.IssuedBy(status.Verifier) {
		t.Fatal("the witness is not minted under the status issuer the policy wiring will bind to")
	}
	if got := strings.Join(f.kinds(t), ","); got != "elevation.attempt,elevation.attempt,elevation.grant" {
		t.Fatalf("audit kinds = %s", got)
	}
	// Replaying the consumed nonce is refused and writes no record: a nonce
	// the ledger no longer holds cannot grow the audit log.
	if _, err := wrapped(context.Background(), f.attestedEnvelope(t, f.ks, args, nonce)); err == nil {
		t.Fatal("a replayed attestation ran")
	}
	if c.runs != 1 {
		t.Fatalf("handler ran %d times after a replay", c.runs)
	}
	if got := strings.Join(f.kinds(t), ","); got != "elevation.attempt,elevation.attempt,elevation.grant" {
		t.Fatalf("a replay wrote audit records: %s", got)
	}
}

// Every refusal below must happen before a nonce is issued or a row written.
func TestNewElevationMiddlewareRefusals(t *testing.T) {
	other := newMemKeystore(t)
	for name, tc := range map[string]struct {
		tier  elevation.CustodyTier
		enrol func(f *elevFixture, t *testing.T)
		nolog bool
	}{
		"no enrolment":         {tier: elevation.CustodyPlatform, enrol: func(*elevFixture, *testing.T) {}},
		"record for other key": {tier: elevation.CustodyPlatform, enrol: func(f *elevFixture, t *testing.T) { f.enroll(t, other) }},
		"tier cannot elevate":  {tier: elevation.CustodyNone, enrol: func(f *elevFixture, t *testing.T) { f.enroll(t, f.ks) }},
		"no audit log":         {tier: elevation.CustodyPlatform, enrol: func(f *elevFixture, t *testing.T) { f.enroll(t, f.ks) }, nolog: true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newElevFixture(t)
			tc.enrol(f, t)
			log := f.log
			if tc.nolog {
				log = nil
			}
			sel := selectorOver(f.dir, tc.tier, f.ks)
			if tc.tier == elevation.CustodyNone {
				sel.Sources = nil
			}
			mw, status := NewElevationMiddleware(f.dir, sel, log, f.clock)
			if !tc.nolog && status.Bound {
				t.Fatalf("status = %+v, want unbound", status)
			}
			c := &elevCounter{}
			_, err := mw("plugin.add", c.handler)(context.Background(), json.RawMessage(`{}`))
			if err == nil || c.runs != 0 {
				t.Fatalf("err=%v runs=%d, want a refusal", err, c.runs)
			}
			if eo, ok := err.(*rpc.ErrorObject); ok && eo.Code == -32008 {
				t.Errorf("handed out a challenge nobody could satisfy: %+v", eo)
			}
			if len(f.kinds(t)) != 0 {
				t.Errorf("a refusal wrote audit rows: %v", f.kinds(t))
			}
			// A plain verb is untouched.
			if _, err := mw("status.get", c.handler)(context.Background(), nil); err != nil || c.runs != 1 {
				t.Errorf("status.get through the refusing middleware: err=%v runs=%d", err, c.runs)
			}
		})
	}
}

func TestRefuseAllElevation(t *testing.T) {
	clock := runtime.NewFixedClock(time.Unix(4000, 0))
	mw := RefuseAllElevation(clock)
	c := &elevCounter{}
	for _, m := range rpc.ElevatedVerbNames() {
		// `[]` is not an object, so every conditional rule elevates it too.
		_, err := mw(m, c.handler)(context.Background(), json.RawMessage(`[]`))
		eo, ok := err.(*rpc.ErrorObject)
		if !ok || eo.Code == -32008 {
			t.Fatalf("%s: err = %#v, want the custody refusal", m, err)
		}
		data, _ := eo.Data.(map[string]string)
		if data["reason"] != rpc.ReasonCustodyTier || data["tier"] != "none" {
			t.Errorf("%s: data = %+v, want custody refusal at tier none", m, eo.Data)
		}
	}
	if c.runs != 0 {
		t.Fatalf("refuse-all let %d elevated calls through", c.runs)
	}
	if _, err := mw("status.get", c.handler)(context.Background(), nil); err != nil || c.runs != 1 {
		t.Fatalf("refuse-all blocked a plain verb: err=%v runs=%d", err, c.runs)
	}
}
