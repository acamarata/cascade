package policy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fixtureKeySeed is the 32-byte Ed25519 seed the checked-in corpus was
// captured under. Provenance is recorded in testdata/approval/README.md.
const fixtureKeySeed = "cascade-approval-fixture-seed-32"

// fixtureIssued is the instant the corpus fixtures were signed at.
var fixtureIssued = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// staticKeys is a key source over one keypair. It is not a stand-in for a
// vault: it is the injection point a vault-backed source occupies, and the
// test needs a key it also holds the public half of.
type staticKeys struct {
	id      string
	private ed25519.PrivateKey
	handed  []ed25519.PrivateKey
}

// ApprovalKey hands back a COPY, which is what the interface requires: the
// signer zeroes what it is given.
func (s *staticKeys) ApprovalKey(context.Context) (string, ed25519.PrivateKey, error) {
	out := make(ed25519.PrivateKey, len(s.private))
	copy(out, s.private)
	s.handed = append(s.handed, out)
	return s.id, out, nil
}

// failingKeys refuses. A signer whose key source fails must return the
// failure, never an unsigned token.
type failingKeys struct{}

// ApprovalKey always fails.
func (failingKeys) ApprovalKey(context.Context) (string, ed25519.PrivateKey, error) {
	return "", nil, errors.New("no key")
}

// signerFixture holds a signer, a verifier over the same real keypair and
// the clock both read.
type signerFixture struct {
	keys   *staticKeys
	clock  *testkit.FrozenClock
	signer *ApprovalSigner
	verify *ApprovalVerifier
	public ed25519.PublicKey
}

// newSignerFixture generates a REAL Ed25519 keypair with the Go standard
// library (Art.2: the external contract is exercised against the real
// counterpart, never a self-authored dialect).
func newSignerFixture(t *testing.T) *signerFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(cryptorand.Reader)
	if err != nil {
		t.Fatalf("generating a real Ed25519 keypair: %v", err)
	}
	f := &signerFixture{
		keys:   &staticKeys{id: "approval-1", private: priv},
		clock:  testkit.NewFrozenClock(fixtureIssued),
		public: pub,
	}
	if f.signer, err = NewApprovalSigner(f.keys, f.clock); err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	if f.verify, err = NewApprovalVerifier(pub, f.clock); err != nil {
		t.Fatalf("building the verifier: %v", err)
	}
	return f
}

// sampleRecord is the caller-supplied half of a record: the fields Sign
// does not overwrite.
func sampleRecord() ApprovalRecord {
	return ApprovalRecord{
		Verb:          "workspace.write",
		ParamsDigest:  "sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0",
		Requester:     "user:owner",
		Approver:      "user:owner",
		Origin:        "cli",
		Scope:         "repo:/workspace",
		Target:        ApprovalTarget{Node: "controller", Session: "S1", Task: "T1"},
		Risk:          L2,
		Sensitivity:   DataClassInternal,
		PolicyVersion: "2026-03-01",
		Audience:      "controller",
	}
}

// TestApprovalTokenRoundTrip proves a signed record verifies against a
// real stdlib keypair, and that Sign filled in the four fields a caller
// must not choose.
func TestApprovalTokenRoundTrip(t *testing.T) {
	f := newSignerFixture(t)
	signed, err := f.signer.Sign(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	rec, err := f.verify.Verify(signed)
	if err != nil {
		t.Fatalf("Verify on a freshly signed token: %v", err)
	}
	if !rec.RequestID.Valid() || !rec.Nonce.Valid() {
		t.Errorf("Sign left the request id or nonce unset: %+v", rec)
	}
	if rec.KeyID != "approval-1" {
		t.Errorf("KeyID = %q, want the key source's id", rec.KeyID)
	}
	if !rec.Issued.Equal(fixtureIssued) {
		t.Errorf("Issued = %s, want the injected clock's instant", rec.Issued)
	}
	if want := fixtureIssued.Add(MaxApprovalTTL); !rec.Expiry.Equal(want) {
		t.Errorf("Expiry = %s, want %s (the five-minute ceiling)", rec.Expiry, want)
	}
}

// TestApprovalTokenNoncesAreUnique proves two signatures of the same
// record carry different request ids and nonces, so one approval can never
// be mistaken for another.
func TestApprovalTokenNoncesAreUnique(t *testing.T) {
	f := newSignerFixture(t)
	first, err := f.signer.Sign(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("first Sign: %v", err)
	}
	second, err := f.signer.Sign(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("second Sign: %v", err)
	}
	a, _ := f.verify.Verify(first)
	b, _ := f.verify.Verify(second)
	if a == nil || b == nil {
		t.Fatal("one of the two signed tokens did not verify")
	}
	if a.RequestID == b.RequestID || a.Nonce == b.Nonce {
		t.Error("two mints produced the same request id or nonce")
	}
}

// TestApprovalToken_Expired is the expiry gate, driven with a clock the
// test controls. Exactly at the expiry instant is ALREADY expired, and one
// nanosecond past it is expired too; the signature is good in both cases,
// which is the point.
func TestApprovalToken_Expired(t *testing.T) {
	f := newSignerFixture(t)
	signed, err := f.signer.Sign(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	expiry := fixtureIssued.Add(MaxApprovalTTL)
	for _, tc := range []struct {
		name string
		at   time.Time
		want bool
	}{
		{"one nanosecond before expiry", expiry.Add(-time.Nanosecond), false},
		{"exactly at expiry", expiry, true},
		{"one nanosecond past expiry", expiry.Add(time.Nanosecond), true},
	} {
		f.clock.Set(tc.at)
		_, verr := f.verify.Verify(signed)
		if tc.want != errors.Is(verr, ErrExpired) {
			t.Errorf("%s: Verify = %v; want ErrExpired == %v", tc.name, verr, tc.want)
		}
	}
}

// TestApprovalTokenSingleByteMutation mutates every byte position of a
// signed token in turn and asserts that none of them verifies. A single
// flipped bit anywhere in the payload or the signature must refuse.
func TestApprovalTokenSingleByteMutation(t *testing.T) {
	f := newSignerFixture(t)
	signed, err := f.signer.Sign(context.Background(), sampleRecord())
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	for i := range signed {
		mutated := append([]byte{}, signed...)
		mutated[i] ^= 0x01
		if rec, verr := f.verify.Verify(mutated); verr == nil || rec != nil {
			t.Fatalf("a token with byte %d flipped verified", i)
		}
	}
}

// TestApprovalTokenDomainSeparation proves a signature over the record's
// canonical bytes WITHOUT the domain prefix does not verify, so a
// signature from another context can never be replayed as an approval.
func TestApprovalTokenDomainSeparation(t *testing.T) {
	f := newSignerFixture(t)
	rec := sampleRecord()
	rec.SchemaVersion = ApprovalSchemaVersion
	rec.KeyID = "approval-1"
	rec.Issued = fixtureIssued
	rec.Expiry = fixtureIssued.Add(MaxApprovalTTL)
	rec.RequestID, _ = cascade.NewID()
	rec.Nonce, _ = cascade.NewID()
	payload := CanonicalEncode(rec)
	undomained := append(append([]byte{}, payload...),
		ed25519.Sign(f.keys.private, payload)...)
	if _, err := f.verify.Verify(undomained); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("a signature taken outside the domain verified: %v", err)
	}
	domained := append(append([]byte{}, payload...),
		ed25519.Sign(f.keys.private, append([]byte(approvalDomain), payload...))...)
	if _, err := f.verify.Verify(domained); err != nil {
		t.Fatalf("a signature taken inside the domain did not verify: %v", err)
	}
}

// TestApprovalTokenVerifyRefusesUnparseable is the fail-closed case for
// the verify entry point: input it cannot understand REFUSES. None of
// these is a signature failure the caller could mistake for a soft error.
func TestApprovalTokenVerifyRefusesUnparseable(t *testing.T) {
	f := newSignerFixture(t)
	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"shorter than a signature", make([]byte, ed25519.SignatureSize)},
		{"not JSON at all", append([]byte("not json"), make([]byte, ed25519.SignatureSize)...)},
		{"a bare JSON array", append([]byte("[1,2,3]"), make([]byte, ed25519.SignatureSize)...)},
		{"a JSON null", append([]byte("null"), make([]byte, ed25519.SignatureSize)...)},
		{"an empty object", append([]byte("{}"), make([]byte, ed25519.SignatureSize)...)},
	} {
		rec, err := f.verify.Verify(tc.input)
		if err == nil || rec != nil {
			t.Errorf("%s: Verify returned %v, %v; unparseable input must refuse", tc.name, rec, err)
		}
	}
}

// TestApprovalSignerRefusesWithoutCollaborators covers the constructor's
// fail-closed guards and a key source that cannot answer.
func TestApprovalSignerRefusesWithoutCollaborators(t *testing.T) {
	clock := testkit.NewFrozenClock(fixtureIssued)
	if _, err := NewApprovalSigner(nil, clock); err == nil {
		t.Error("a signer with no key source was built")
	}
	if _, err := NewApprovalSigner(&staticKeys{}, nil); err == nil {
		t.Error("a signer with no clock was built")
	}
	if _, err := NewApprovalVerifier(ed25519.PublicKey("short"), clock); err == nil {
		t.Error("a verifier over a malformed public key was built")
	}
	if _, err := NewApprovalVerifier(make(ed25519.PublicKey, ed25519.PublicKeySize), nil); err == nil {
		t.Error("a verifier with no clock was built")
	}
	s, err := NewApprovalSigner(failingKeys{}, clock)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	if _, err := s.Sign(context.Background(), sampleRecord()); err == nil {
		t.Error("Sign produced a token from a key source that failed")
	}
	short, err := NewApprovalSigner(&staticKeys{id: "k", private: ed25519.PrivateKey("short")}, clock)
	if err != nil {
		t.Fatalf("building the signer: %v", err)
	}
	if _, err := short.Sign(context.Background(), sampleRecord()); err == nil {
		t.Error("Sign produced a token from a key of the wrong length")
	}
}

// TestApprovalSignerZeroesKeyMaterial proves the private key the signer
// was handed is zeroed by the time Sign returns.
func TestApprovalSignerZeroesKeyMaterial(t *testing.T) {
	f := newSignerFixture(t)
	if _, err := f.signer.Sign(context.Background(), sampleRecord()); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(f.keys.handed) != 1 {
		t.Fatalf("the key source was consulted %d times, want once", len(f.keys.handed))
	}
	if !bytes.Equal(f.keys.handed[0], make([]byte, ed25519.PrivateKeySize)) {
		t.Error("the private key the signer was handed still holds key material")
	}
}
