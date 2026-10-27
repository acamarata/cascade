package runtime

// Purpose: proves a looser config is admitted only by a v2 record whose
// proof verifies over the exact file digest: unsigned rows, proofs for other
// bytes and proofs signed by another key all fail closed.
// Inputs: an in-memory store, synthetic configs, a test verifier that checks
// a real Ed25519 signature over the proof params against one trusted key.
// Outputs: none (tests).
// Constraints: no keychain; fixture digests are synthetic.
// SPORT: runtime/boot-baseline-check tests (P1-SEC-04).

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

// looserThanFloor is a config the defaults refuse.
var looserThanFloor = EffectiveConfig{
	Sync:      SyncSection{Classes: map[string]string{}},
	Conductor: ConductorSection{ExternalRoutingEnabled: true},
}

// testSigner signs proofs; trustedVerifier accepts only signatures by key.
type testSigner struct{ priv ed25519.PrivateKey }

func newTestSigner(t *testing.T) (testSigner, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return testSigner{priv: priv}, pub
}

func (s testSigner) proof(t *testing.T, digest string) BaselineProof {
	t.Helper()
	params := []byte(`{"config_sha256":"` + digest + `"}`)
	att, err := json.Marshal(map[string][]byte{"sig": ed25519.Sign(s.priv, params)})
	if err != nil {
		t.Fatal(err)
	}
	return BaselineProof{Method: AcceptLooseningMethod, RawParams: params, Attestation: att}
}

func trustedVerifier(key ed25519.PublicKey) BaselineVerifier {
	return func(p BaselineProof) error {
		var att map[string][]byte
		if err := json.Unmarshal(p.Attestation, &att); err != nil {
			return err
		}
		if !ed25519.Verify(key, p.RawParams, att["sig"]) {
			return errBaselineProof("test: signature does not verify")
		}
		return nil
	}
}

// putRecord writes a raw baseline row and a snapshot, the way an attacker
// with store access would.
func putRecord(t *testing.T, store *storetest.MemStore, rec any, snap EffectiveConfig) {
	t.Helper()
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), auditNamespace, baselineRecordKey, data); err != nil {
		t.Fatal(err)
	}
	s, _ := json.Marshal(snap)
	if err := store.Put(context.Background(), auditNamespace, baselineSnapshotKey, s); err != nil {
		t.Fatal(err)
	}
}

func checkerWith(t *testing.T, v BaselineVerifier) (*BaselineChecker, *storetest.MemStore) {
	t.Helper()
	bc, store, _ := newTestBaselineChecker(t)
	bc.SetVerifier(v)
	return bc, store
}

const fileDigestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fileDigestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func assertRestrictive(t *testing.T, res BaselineResult, want BaselineOutcome) {
	t.Helper()
	if res.Outcome != want {
		t.Fatalf("outcome = %s, want %s", res.Outcome.Detail(), want.Detail())
	}
	if res.Effective.Conductor.ExternalRoutingEnabled {
		t.Fatal("the looser on-disk value ran; want the most-restrictive one")
	}
	if !res.Outcome.Degraded() {
		t.Fatalf("%s is not reported degraded", res.Outcome.Detail())
	}
}

func TestForgedUnsignedBaselineRefused(t *testing.T) {
	hash := ComputeSectionsHash(looserThanFloor)
	pass := func(BaselineProof) error { return nil } // even an accept-all verifier must not admit a row with no proof
	for name, rec := range map[string]any{
		"v1 that exists and parses": baselineRecordV1{Version: 1, SectionsHash: hash, Sections: baselineGuardedSections},
		"v2 without proof":          baselineRecordV1{Version: 2, SectionsHash: hash, Sections: baselineGuardedSections, ConfigSHA256: fileDigestA},
	} {
		t.Run(name, func(t *testing.T) {
			bc, store := checkerWith(t, pass)
			putRecord(t, store, rec, looserThanFloor)
			assertRestrictive(t, bc.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)), BaselineUnsignedLooser)
		})
	}
}

func TestBaselineProofForOtherFileRefused(t *testing.T) {
	signer, pub := newTestSigner(t)
	hash := ComputeSectionsHash(looserThanFloor)
	signed := func(proofDigest, recordDigest string) baselineRecordV1 {
		p := signer.proof(t, proofDigest)
		return baselineRecordV1{Version: 2, SectionsHash: hash, Sections: baselineGuardedSections, ConfigSHA256: recordDigest,
			Proof: &baselineProofDoc{Method: p.Method, RawParams: p.RawParams, Attestation: p.Attestation}}
	}
	// Positive control: the same record admits the file it covers.
	bc, store := checkerWith(t, trustedVerifier(pub))
	putRecord(t, store, signed(fileDigestA, fileDigestA), looserThanFloor)
	if res := bc.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)); res.Outcome != BaselineAccepted || !res.Effective.Conductor.ExternalRoutingEnabled {
		t.Fatalf("a valid proof over this file: outcome %s, want baseline_accepted with the on-disk values", res.Outcome.Detail())
	}
	for name, rec := range map[string]baselineRecordV1{
		"proof and record name another file": signed(fileDigestB, fileDigestB),
		"record restates this file":          signed(fileDigestB, fileDigestA),
	} {
		t.Run(name, func(t *testing.T) {
			bc, store := checkerWith(t, trustedVerifier(pub))
			putRecord(t, store, rec, looserThanFloor)
			assertRestrictive(t, bc.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)), BaselineProofInvalid)
		})
	}
	t.Run("no file digest", func(t *testing.T) {
		bc, store := checkerWith(t, trustedVerifier(pub))
		putRecord(t, store, signed(fileDigestA, fileDigestA), looserThanFloor)
		assertRestrictive(t, bc.Check(context.Background(), looserThanFloor), BaselineProofInvalid)
	})
	t.Run("no verifier", func(t *testing.T) {
		bc, store := checkerWith(t, nil)
		putRecord(t, store, signed(fileDigestA, fileDigestA), looserThanFloor)
		assertRestrictive(t, bc.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)), BaselineProofInvalid)
	})
}

func TestBaselineProofOtherKeyRefused(t *testing.T) {
	_, trusted := newTestSigner(t)
	other, _ := newTestSigner(t)
	p := other.proof(t, fileDigestA)
	rec := baselineRecordV1{Version: 2, SectionsHash: ComputeSectionsHash(looserThanFloor), Sections: baselineGuardedSections, ConfigSHA256: fileDigestA,
		Proof: &baselineProofDoc{Method: p.Method, RawParams: p.RawParams, Attestation: p.Attestation}}
	bc, store := checkerWith(t, trustedVerifier(trusted))
	putRecord(t, store, rec, looserThanFloor)
	assertRestrictive(t, bc.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)), BaselineProofInvalid)

	// A proof for another method over the same params is refused too.
	rec.Proof.Method = "vault.get"
	bc2, store2 := checkerWith(t, func(BaselineProof) error { return nil })
	putRecord(t, store2, rec, looserThanFloor)
	assertRestrictive(t, bc2.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)), BaselineProofInvalid)
}

func TestDecodeAcceptParamsIsStrict(t *testing.T) {
	for _, raw := range []string{
		`{"config_sha256":"` + fileDigestA + `","x":1}`, `{"config_sha256":"ABC"}`, `{}`,
		`{"config_sha256":"` + fileDigestA + `"} {}`, `[]`,
	} {
		if _, ok := DecodeAcceptParams([]byte(raw)); ok {
			t.Errorf("DecodeAcceptParams(%s) accepted", raw)
		}
	}
	if got, ok := DecodeAcceptParams([]byte(`{"config_sha256":"` + fileDigestA + `"}`)); !ok || got != fileDigestA {
		t.Fatalf("valid params refused: %q %v", got, ok)
	}
}

func TestBaselineFirstBootAndUnknownOutcome(t *testing.T) {
	bc, store, _ := newTestBaselineChecker(t)
	floor := MostRestrictiveDefaults()
	if res := bc.CheckFile(context.Background(), floor, signedAs(fileDigestA, floor)); res.Outcome != BaselineFirstBootAccepted || res.Outcome.Degraded() {
		t.Fatalf("first boot at the defaults: %s", res.Outcome.Detail())
	}
	if _, err := store.Get(context.Background(), auditNamespace, baselineRecordKey); err != nil {
		t.Fatalf("first boot wrote no record: %v", err)
	}
	if res := bc.CheckFile(context.Background(), floor, signedAs(fileDigestA, floor)); res.Outcome != BaselineOK {
		t.Fatalf("second boot: %s, want baseline_ok", res.Outcome.Detail())
	}
	if d := BaselineOutcome(99).Detail(); d != "baseline_divergent" || !BaselineOutcome(99).Degraded() {
		t.Fatalf("unknown outcome reads %q", d)
	}
}

// signedAs is a BaselineFile whose bytes hash to digest and declare eff.
func signedAs(digest string, eff EffectiveConfig) BaselineFile {
	return BaselineFile{SHA256: digest, Signed: func() (EffectiveConfig, bool) { return eff, true }}
}

func TestBaselineForgedSectionsHashWithValidSignatureRefused(t *testing.T) {
	signer, pub := newTestSigner(t)
	// The owner accepted a file declaring external routing only.
	accepted := looserThanFloor
	// Running config: also spill (an env override, or a swapped file), with
	// the record's unsigned sections_hash forged to match it.
	running := looserThanFloor
	running.Conductor.SpillEnabled = true
	p := signer.proof(t, fileDigestA)
	rec := baselineRecordV1{Version: 2, SectionsHash: ComputeSectionsHash(running), Sections: baselineGuardedSections, ConfigSHA256: fileDigestA,
		Proof: &baselineProofDoc{Method: p.Method, RawParams: p.RawParams, Attestation: p.Attestation}}
	bc, store := checkerWith(t, trustedVerifier(pub))
	putRecord(t, store, rec, running)
	res := bc.CheckFile(context.Background(), running, signedAs(fileDigestA, accepted))
	if res.Outcome != BaselineUnsignedLooser || res.Effective.Conductor.SpillEnabled || res.Effective.Conductor.ExternalRoutingEnabled {
		t.Fatalf("forged sections_hash with a valid signature: %s spill=%v", res.Outcome.Detail(), res.Effective.Conductor.SpillEnabled)
	}
	// The signed bytes failing to load admit nothing either.
	unloadable := BaselineFile{SHA256: fileDigestA, Signed: func() (EffectiveConfig, bool) { return EffectiveConfig{}, false }}
	if res := bc.CheckFile(context.Background(), running, unloadable); res.Outcome != BaselineProofInvalid {
		t.Fatalf("unloadable signed bytes: %s", res.Outcome.Detail())
	}
}

func TestBaselinePersistFailureFailsClosed(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		store := &failingPutStore{MemStore: storetest.NewMemStore(), failAt: failAt}
		bc := NewBaselineChecker(store, NewFixedClock(fixedEpoch), nil, nil)
		res := bc.CheckFile(context.Background(), MostRestrictiveDefaults(), BaselineFile{SHA256: fileDigestA})
		if res.Outcome != BaselineMissing || !res.Outcome.Degraded() {
			t.Fatalf("first boot, Put %d failed: %s, want baseline_missing", failAt, res.Outcome.Detail())
		}
	}
	// Tightening whose rewrite fails is not reported as tightened.
	tighter := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "local-only"}}}
	loose := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "server-primary"}}}
	store := &failingPutStore{MemStore: storetest.NewMemStore(), failAt: 3}
	putRecord(t, store.MemStore, baselineRecordV1{Version: 1, SectionsHash: ComputeSectionsHash(loose), Sections: baselineGuardedSections}, loose)
	bc := NewBaselineChecker(store, NewFixedClock(fixedEpoch), nil, nil)
	store.puts = 2
	if res := bc.CheckFile(context.Background(), tighter, BaselineFile{}); res.Outcome != BaselineDivergent {
		t.Fatalf("tightening rewrite failed: %s, want baseline_divergent", res.Outcome.Detail())
	}
}
