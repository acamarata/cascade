package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

const fakeSecretValue = "fake-secret-value-0000" // obviously fake

// recKeystore is a real ed25519 test key whose Sign can be overridden; it
// counts and keeps every payload it was asked to sign.
type recKeystore struct {
	signingKeystore
	mu     sync.Mutex
	signs  int
	signed [][]byte
	sign   func([]byte) ([]byte, error)
}

func (k *recKeystore) Sign(p []byte) ([]byte, error) {
	k.mu.Lock()
	k.signs++
	k.signed = append(k.signed, append([]byte(nil), p...))
	k.mu.Unlock()
	if k.sign != nil {
		return k.sign(p)
	}
	return k.signingKeystore.Sign(p)
}

// tapWriter forwards to a real audit log and keeps every committed record.
type tapWriter struct {
	inner audit.Writer
	fail  error
	mu    sync.Mutex
	recs  []audit.Record
}

func (w *tapWriter) Append(ctx context.Context, e audit.Event) (audit.Record, error) {
	if w.fail != nil {
		return audit.Record{}, w.fail
	}
	rec, err := w.inner.Append(ctx, e)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err == nil {
		w.recs = append(w.recs, rec)
	}
	return rec, err
}

func (w *tapWriter) kinds() (out []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range w.recs {
		out = append(out, string(r.Kind))
	}
	return out
}

// gateFixture is the production gate over a fake key enrolled in a temp
// data dir, a real audit log over a memory store and a counted custody.
type gateFixture struct {
	gate     *elevationGate
	ks       *recKeystore
	tap      *tapWriter
	custodys int
}

func newGateFixture(t *testing.T, env map[string]string) *gateFixture {
	t.Helper()
	f := &gateFixture{ks: &recKeystore{signingKeystore: newSigningKeystore(t)}}
	clock := runtime.NewSystemClock()
	dir := t.TempDir()
	pub, _ := f.ks.PubKeyB64()
	if _, err := elevation.NewElevationTrustStore(elevation.NewFileBackend(dir), clock).Enroll(pub); err != nil {
		t.Fatalf("enroll the fake key: %v", err)
	}
	selectCustody := testCustody(t, f.ks, elevation.CustodyPlatform)
	f.tap = &tapWriter{inner: audit.New(storetest.NewMemStore(), clock, nil)}
	f.gate = newElevationGate(func() elevation.Custody { f.custodys++; return selectCustody() },
		func() string { return dir }, f.tap, clock, func(k string) string { return env[k] })
	return f
}

// spyCustody counts broker reads.
type spyCustody struct {
	secrets.Custody
	gets int
}

func (s *spyCustody) Get(ctx context.Context, name string) ([]byte, error) {
	s.gets++
	return s.Custody.Get(ctx, name)
}

// seededVault stores fakeSecretValue under FAKE_NAME and returns deps whose
// broker reads go through the spy.
func seededVault(t *testing.T, gate secrets.ElevationGate) (vaultDeps, *spyCustody) {
	t.Helper()
	deps := testVaultDeps(t, okGate{}, nil)
	if _, _, err := runVault(t, deps, fakeSecretValue+"\n", "set", "FAKE_NAME"); err != nil {
		t.Fatalf("set: %v", err)
	}
	inner, err := deps.NewCustody()
	if err != nil {
		t.Fatal(err)
	}
	spy := &spyCustody{Custody: inner}
	deps.NewCustody = func() (secrets.Custody, error) { return spy, nil }
	deps.Gate = gate
	return deps, spy
}

func TestVaultGetRefusesWithoutSignature(t *testing.T) {
	skipWithoutLocalElevation(t)
	for label, tc := range map[string]struct {
		sign   func([]byte) ([]byte, error)
		reason string
		text   string
	}{
		"nil-nil": {func([]byte) ([]byte, error) { return nil, nil }, `"reason":"no_signature"`, "returned no signature"},
		"error":   {func([]byte) ([]byte, error) { return nil, errors.New("fake: presence refused") }, `"reason":"sign_failed"`, "fake: presence refused"},
	} {
		f := newGateFixture(t, nil)
		f.ks.sign = tc.sign
		deps, spy := seededVault(t, f.gate)
		stdout, _, err := runVault(t, deps, "", "get", "FAKE_NAME")
		if !isCLIKind(err, cascade.KindElevationRequired) || stdout != "" || spy.gets != 0 {
			t.Fatalf("%s: get = (%q, %v), broker gets %d; want ELEVATION_REQUIRED and no read", label, stdout, err, spy.gets)
		}
		if !strings.Contains(err.Error(), tc.text) || f.ks.signs != 1 {
			t.Fatalf("%s: refusal %q (signs %d) is not the unsigned-challenge refusal", label, err, f.ks.signs)
		}
		if got := strings.Join(f.tap.kinds(), ","); got != "vault.access,elevation.attempt,elevation.deny" ||
			!strings.Contains(string(f.tap.recs[2].Explain), tc.reason) {
			t.Fatalf("%s: audit rows %s %s", label, got, f.tap.recs[len(f.tap.recs)-1].Explain)
		}
	}
}

func TestVaultGetSignsOverVerb(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	deps, spy := seededVault(t, f.gate)
	stdout, _, err := runVault(t, deps, "", "get", "FAKE_NAME")
	if err != nil || stdout != fakeSecretValue || spy.gets != 1 || f.ks.signs != 1 {
		t.Fatalf("get = (%q, %v) gets=%d signs=%d", stdout, err, spy.gets, f.ks.signs)
	}
	var signed struct {
		Method     string `json:"method"`
		ActionHash string `json:"action_hash"`
		Nonce      string `json:"nonce"`
	}
	if err := json.Unmarshal(f.ks.signed[0], &signed); err != nil {
		t.Fatalf("signed bytes are not the attestation object: %v", err)
	}
	wantHash := rpc.ParamsHash(json.RawMessage(`{"verb":"vault.get","local":true}`))
	if signed.Method != "vault.get" || signed.ActionHash != wantHash || signed.Nonce == "" {
		t.Fatalf("signed %+v, want method vault.get over %s with a nonce", signed, wantHash)
	}
}

func TestVaultGetNonceSingleUse(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	var first rpc.Attestation
	f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, m, h, n string) (rpc.Attestation, error) {
		att, err := signAttestation(ks, c, m, h, n)
		first = att
		return att, err
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); err != nil {
		t.Fatalf("positive control refused: %v", err)
	}
	f.gate.attest = func(elevation.ElevationKeystore, runtime.Clock, string, string, string) (rpc.Attestation, error) {
		return first, nil
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), "nonce not found") {
		t.Fatalf("a replayed attestation = %v, want the nonce-not-found refusal", err)
	}
	f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, m, h, _ string) (rpc.Attestation, error) {
		return signAttestation(ks, c, m, h, first.Nonce)
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); !cascade.HasKind(err, cascade.KindElevationRequired) || !strings.Contains(err.Error(), "nonce not found") {
		t.Fatalf("a valid signature over another call's nonce = %v, want the nonce-not-found refusal", err)
	}
	deps, spy := seededVault(t, f.gate)
	if _, _, err := runVault(t, deps, "", "get", "FAKE_NAME"); err == nil || spy.gets != 0 {
		t.Fatalf("vault get with a misdirected nonce = %v, broker gets %d", err, spy.gets)
	}
}

func TestNoInputRefusesBeforeKeystoreOpen(t *testing.T) {
	f := newGateFixture(t, map[string]string{"CASCADE_NO_INPUT": "1"})
	deps, spy := seededVault(t, f.gate)
	_, _, err := runVault(t, deps, "", "get", "FAKE_NAME")
	if !isCLIKind(err, cascade.KindElevationRequired) || f.custodys != 0 || f.ks.signs != 0 || spy.gets != 0 || strings.Join(f.tap.kinds(), ",") != "vault.access,elevation.attempt,elevation.deny" {
		t.Fatalf("no-input get = %v; custody calls %d, signs %d, gets %d, rows %v", err, f.custodys, f.ks.signs, spy.gets, f.tap.kinds())
	}
	f.gate.getenv = nil
	if err := f.gate.Authorize(t.Context(), "vault.get"); !cascade.HasKind(err, cascade.KindElevationRequired) || f.custodys != 0 {
		t.Fatalf("an unreadable environment = %v, custody calls %d", err, f.custodys)
	}
}

func TestVaultAccessAudited(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	deps, _ := seededVault(t, f.gate)
	if _, _, err := runVault(t, deps, "", "get", "FAKE_NAME"); err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "vault.access,elevation.attempt,elevation.attempt,elevation.grant"
	if got := strings.Join(f.tap.kinds(), ","); got != want {
		t.Fatalf("audit rows %s, want %s", got, want)
	}
	access := f.tap.recs[0]
	if access.Action != "vault.get" || string(access.Explain) != `{"name":"FAKE_NAME","source":"cli-gate","verb":"vault.get"}` {
		t.Fatalf("vault.access row %s %s", access.Action, access.Explain)
	}
	all, _ := json.Marshal(f.tap.recs)
	if strings.Contains(string(all), fakeSecretValue) {
		t.Fatal("an audit row carries the secret value")
	}
}

func TestGateAuditFailureRefuses(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	f.tap.fail = errors.New("fake: audit store unavailable")
	deps, spy := seededVault(t, f.gate)
	if stdout, _, err := runVault(t, deps, "", "get", "FAKE_NAME"); err == nil || stdout != "" || spy.gets != 0 || f.ks.signs != 0 {
		t.Fatalf("get with a failing audit = (%q, %v) gets=%d signs=%d", stdout, err, spy.gets, f.ks.signs)
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); !cascade.HasKind(err, cascade.KindElevationRequired) || f.ks.signs != 0 {
		t.Fatalf("a failing elevation audit = %v, signs %d", err, f.ks.signs)
	}
	f.gate.sink = nil
	if err := f.gate.Authorize(t.Context(), "vault.get"); !cascade.HasKind(err, cascade.KindElevationRequired) || f.ks.signs != 0 {
		t.Fatalf("a nil audit writer = %v, signs %d", err, f.ks.signs)
	}
}

func TestLocalGateNeverPassesThroughWithoutSignature(t *testing.T) {
	skipWithoutLocalElevation(t)
	f := newGateFixture(t, nil)
	pub := f.ks.pub
	fp, _ := elevation.Fingerprint(mustPub(t, f.ks))
	trust := rpc.MapTrustStore{fp: pub}
	// The middleware passes an unlisted verb through; the guarded handler
	// must still refuse it, because no witness was minted.
	for _, verb := range []string{"local.vault.get", "node.git_gate_install", "status.get"} {
		params, hash, _ := localChallenge(verb)
		if err := f.gate.ceremony(t.Context(), verb, params, hash, elevation.CustodyPlatform, f.ks, trust, &gateAuditor{sink: f.tap}); !cascade.HasKind(err, cascade.KindElevationRequired) {
			t.Fatalf("%s passed through the ceremony: %v", verb, err)
		}
	}
	if _, err := witnessGuard(rpc.NewWitnessIssuer().Verifier(), "vault.get", "fake-hash")(t.Context(), nil); err == nil {
		t.Fatal("the guarded handler granted with no witness")
	}
	f.gate.attest = func(ks elevation.ElevationKeystore, c runtime.Clock, m, h, n string) (rpc.Attestation, error) {
		att, err := signAttestation(ks, c, m, h, n)
		att.SigB64 = ""
		return att, err
	}
	if err := f.gate.Authorize(t.Context(), "vault.get"); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("an attestation with no signature = %v", err)
	}
	if f.gate.attest = nil; f.gate.Authorize(t.Context(), "vault.get") != nil {
		t.Fatal("positive control refused")
	}
}
