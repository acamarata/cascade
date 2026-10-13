// Purpose: tests for ProjectEnvelope: a field-for-field copy of a verified
// envelope (reflection over every SpawnEnvelope field), and every kind of
// unverifiable envelope refusing with envelope.Verify's own error.
// SPORT: internal.providers.agents tests (ADD) - P1-AGT-02.

package agents

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/envelope"
	"github.com/acamarata/cascade/pkg/provider"
)

var t0 = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// fixedClock reports t0.
type fixedClock struct{}

func (fixedClock) Now() time.Time { return t0 }

// keySigner is an in-memory envelope.Signer over a seed-derived key; no
// platform keystore is touched.
type keySigner struct{ priv ed25519.PrivateKey }

func newKeySigner(fill byte) keySigner {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = fill
	}
	return keySigner{priv: ed25519.NewKeyFromSeed(seed)}
}

func (k keySigner) PublicKey() ed25519.PublicKey { return k.priv.Public().(ed25519.PublicKey) }

func (k keySigner) Sign(_ context.Context, msg []byte) ([]byte, error) {
	return ed25519.Sign(k.priv, msg), nil
}

// testEnvelope has every projected field populated.
func testEnvelope() envelope.Envelope {
	return envelope.Envelope{
		Version: envelope.Version, ID: "env-1", Principal: "principal-1",
		Roots: []string{"/srv/a", "/srv/b"}, Tools: []string{"edit", "read"}, Commands: []string{"git", "go"},
		Egress: []string{"model", "vcs"}, Budget: envelope.Budget{WallClock: time.Hour, Tokens: 10, CostMicros: 5, Procs: 2},
		Fence:       envelope.Fence{DispatchID: "d1", Attempt: 1, LeaseEpoch: 3},
		Sensitivity: provider.SensitivityInternal, IssuedAt: t0.Add(-time.Hour), ExpiresAt: t0.Add(time.Hour),
	}
}

func issue(t *testing.T, s keySigner, e envelope.Envelope) envelope.Signed {
	t.Helper()
	signed, err := envelope.Issue(context.Background(), s, e)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return signed
}

func TestProjectEnvelopeCopiesEveryField(t *testing.T) {
	signer := newKeySigner(1)
	signed := issue(t, signer, testEnvelope())
	got, err := ProjectEnvelope(signed, signer.PublicKey(), t0)
	if err != nil {
		t.Fatalf("ProjectEnvelope: %v", err)
	}
	canon, err := envelope.Canonical(signed.Env)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canon)
	want := provider.SpawnEnvelope{Digest: hex.EncodeToString(sum[:]), Principal: "principal-1",
		Roots: []string{"/srv/a", "/srv/b"}, Tools: []string{"edit", "read"}, Commands: []string{"git", "go"},
		Egress: []string{"model", "vcs"}, Sensitivity: provider.SensitivityInternal, ExpiresAt: t0.Add(time.Hour)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection\n%+v\nwant\n%+v", got, want)
	}
	v := reflect.ValueOf(got)
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("SpawnEnvelope.%s was not set from the envelope", v.Type().Field(i).Name)
		}
	}
	got.Roots[0], got.Tools[0] = "/changed", "changed"
	if signed.Env.Roots[0] != "/srv/a" || signed.Env.Tools[0] != "edit" {
		t.Fatal("the projection aliases the signed envelope's slices")
	}
}

func TestProjectEnvelopeRefusesUnverifiable(t *testing.T) {
	signer, foreign := newKeySigner(1), newKeySigner(2)
	good := issue(t, signer, testEnvelope())
	unsigned := good
	unsigned.Sig = nil
	tampered := issue(t, signer, testEnvelope())
	tampered.Env.Principal = "someone-else"
	expired := good
	cases := map[string]struct {
		s    envelope.Signed
		now  time.Time
		kind cascade.Kind
	}{
		"unsigned":       {unsigned, t0, cascade.KindIntegrity},
		"tampered":       {tampered, t0, cascade.KindIntegrity},
		"foreign-signed": {issue(t, foreign, testEnvelope()), t0, cascade.KindIntegrity},
		"expired":        {expired, t0.Add(2 * time.Hour), cascade.KindPermissionDenied},
		"not yet valid":  {good, t0.Add(-2 * time.Hour), cascade.KindPermissionDenied},
	}
	for name, c := range cases {
		_, verr := envelope.Verify(c.s, signer.PublicKey(), c.now)
		got, err := ProjectEnvelope(c.s, signer.PublicKey(), c.now)
		if err == nil || verr == nil || err.Error() != verr.Error() || !cascade.HasKind(err, c.kind) {
			t.Errorf("%s: ProjectEnvelope error %v, Verify error %v, want the same error of kind %v", name, err, verr, c.kind)
		}
		if !reflect.DeepEqual(got, provider.SpawnEnvelope{}) {
			t.Errorf("%s: a refused projection returned %+v", name, got)
		}
	}
	if _, err := ProjectEnvelope(good, ed25519.PublicKey("short"), t0); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("a pinned key of the wrong length = %v, want KindIntegrity", err)
	}
}
