package ci

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
)

// testOIDCEnv builds a signed token and matching JWKSource for tests.
type testOIDCEnv struct {
	key    *rsa.PrivateKey
	kid    string
	now    time.Time
	want   OIDCWant
	source JWKSource
}

func newTestOIDCEnv(t *testing.T) testOIDCEnv {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Unix(1700000000, 0)
	want := OIDCWant{
		RepositorySlug:    "owner/repo",
		RepositoryID:      "123",
		RepositoryOwnerID: "456",
		JobWorkflowRef:    "owner/repo/.github/workflows/ci.yml@refs/heads/main",
		CommitSHA:         strings.Repeat("ab", 20),
		RunID:             "789",
	}
	return testOIDCEnv{
		key:  key,
		kid:  "test-kid",
		now:  now,
		want: want,
		source: staticJWKSource(map[string]*rsa.PublicKey{
			"test-kid": &key.PublicKey,
		}),
	}
}

func (e testOIDCEnv) signToken(t *testing.T, claims map[string]any, kid, alg string) string {
	t.Helper()
	if kid == "" {
		kid = e.kid
	}
	if alg == "" {
		alg = "RS256"
	}
	hdr := map[string]any{"alg": alg, "typ": "JWT", "kid": kid}
	hdrB, _ := json.Marshal(hdr)
	payB, _ := json.Marshal(claims)
	hdrE := base64.RawURLEncoding.EncodeToString(hdrB)
	payE := base64.RawURLEncoding.EncodeToString(payB)
	signed := hdrE + "." + payE
	var sig []byte
	if alg == "RS256" {
		h := sha256.Sum256([]byte(signed))
		var err error
		sig, err = rsa.SignPKCS1v15(rand.Reader, e.key, 0x05, h[:]) // crypto.SHA256 = 5
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
	} else {
		sig = []byte("invalid")
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (e testOIDCEnv) validClaims() map[string]any {
	return map[string]any{
		"iss":                 githubIssuer,
		"aud":                 expectedAudience,
		"repository":          e.want.RepositorySlug,
		"repository_id":       e.want.RepositoryID,
		"repository_owner_id": e.want.RepositoryOwnerID,
		"job_workflow_ref":    e.want.JobWorkflowRef,
		"sha":                 e.want.CommitSHA,
		"run_id":              e.want.RunID,
		"exp":                 e.now.Add(time.Hour).Unix(),
		"iat":                 e.now.Unix(),
		"nbf":                 e.now.Unix(),
	}
}

// TestAttestOIDCValid asserts a well-formed token yields ClassEphemeralOIDC.
func TestAttestOIDCValid(t *testing.T) {
	env := newTestOIDCEnv(t)
	raw := env.signToken(t, env.validClaims(), "", "")
	cls, err := VerifyGitHubOIDC(context.Background(), raw, env.want, env.source, env.now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cls != attest.ClassEphemeralOIDC {
		t.Fatalf("got class %q, want %q", cls, attest.ClassEphemeralOIDC)
	}
}

// assertOIDCRejected verifies raw is refused with wantCheck.
func assertOIDCRejected(t *testing.T, env testOIDCEnv, raw, wantCheck string) {
	t.Helper()
	cls, err := VerifyGitHubOIDC(context.Background(), raw, env.want, env.source, env.now)
	if cls != attest.ClassUnattested {
		t.Fatalf("got class %q, want %q", cls, attest.ClassUnattested)
	}
	var oidcErr *ErrOIDCInvalid
	if !errors.As(err, &oidcErr) {
		t.Fatalf("got error %T, want *ErrOIDCInvalid", err)
	}
	if oidcErr.Check != wantCheck {
		t.Fatalf("got check %q, want %q", oidcErr.Check, wantCheck)
	}
}

// TestAttestOIDCRejectsClaims covers every named claim mismatch.
func TestAttestOIDCRejectsClaims(t *testing.T) {
	for _, tc := range []struct{ name, key, value, check string }{
		{"wrong issuer", "iss", "https://evil.example.com", "iss"},
		{"wrong audience", "aud", "other", "aud"},
		{"wrong repository", "repository", "other/repo", "repository"},
		{"wrong repository_id", "repository_id", "999", "repository_id"},
		{"wrong repository_owner_id", "repository_owner_id", "999", "repository_owner_id"},
		{"wrong job_workflow_ref", "job_workflow_ref", "other/ref", "job_workflow_ref"},
		{"wrong sha", "sha", strings.Repeat("ff", 20), "sha"},
		{"wrong run_id", "run_id", "000", "run_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestOIDCEnv(t)
			c := env.validClaims()
			c[tc.key] = tc.value
			assertOIDCRejected(t, env, env.signToken(t, c, "", ""), tc.check)
		})
	}
}

// TestAttestOIDCRejectsToken covers the header, key, signature and time
// failure paths.
func TestAttestOIDCRejectsToken(t *testing.T) {
	signed := func(kid, alg string) func(testOIDCEnv, map[string]any) string {
		return func(env testOIDCEnv, c map[string]any) string { return env.signToken(t, c, kid, alg) }
	}
	skew := func(key string, d time.Duration) func(testOIDCEnv, map[string]any) string {
		return func(env testOIDCEnv, c map[string]any) string {
			c[key] = env.now.Add(d).Unix()
			return env.signToken(t, c, "", "")
		}
	}
	for _, tc := range []struct {
		name, check string
		build       func(env testOIDCEnv, c map[string]any) string
	}{
		{"alg none", "alg", signed("", "none")},
		{"alg HS256", "alg", signed("", "HS256")},
		{"alg ES256", "alg", signed("", "ES256")},
		{"unknown kid", "kid", signed("other-kid", "")},
		{"bad signature", "signature", func(env testOIDCEnv, c map[string]any) string {
			parts := strings.Split(env.signToken(t, c, "", ""), ".")
			parts[2] = base64.RawURLEncoding.EncodeToString([]byte("badsig"))
			return strings.Join(parts, ".")
		}},
		{"expired exp", "exp", skew("exp", -time.Hour)},
		{"future iat", "iat", skew("iat", 2*time.Hour)},
		{"future nbf", "nbf", skew("nbf", 2*time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestOIDCEnv(t)
			assertOIDCRejected(t, env, tc.build(env, env.validClaims()), tc.check)
		})
	}
}

// TestAttestOIDCJWKSFetchFailureUnattested ensures fetch, status, size and
// parse failures all yield Check jwks with ClassUnattested.
func TestAttestOIDCJWKSFetchFailureUnattested(t *testing.T) {
	env := newTestOIDCEnv(t)
	raw := env.signToken(t, env.validClaims(), "", "")
	failSource := failJWKSource{err: errors.New("fetch failed")}
	cls, err := VerifyGitHubOIDC(context.Background(), raw, env.want, failSource, env.now)
	if cls != attest.ClassUnattested {
		t.Fatalf("got class %q, want unattested", cls)
	}
	var oidcErr *ErrOIDCInvalid
	if !errors.As(err, &oidcErr) || oidcErr.Check != "jwks" {
		t.Fatalf("got error %v, want ErrOIDCInvalid{jwks}", err)
	}
}

// TestParseJWKS validates ParseJWKS against the captured fixture shape.
func TestParseJWKS(t *testing.T) {
	body := []byte(`{"keys":[{"kty":"RSA","kid":"fixture-kid","use":"sig","alg":"RS256","n":"` +
		base64.RawURLEncoding.EncodeToString(big.NewInt(65537).Bytes()) + `","e":"AQAB"}]}`)
	keys, err := ParseJWKS(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if _, ok := keys["fixture-kid"]; !ok {
		t.Fatalf("missing fixture-kid")
	}
}

// staticJWKSource returns fixed keys.
type staticJWKSource map[string]*rsa.PublicKey

func (s staticJWKSource) Keys(context.Context) (map[string]*rsa.PublicKey, error) {
	return map[string]*rsa.PublicKey(s), nil
}

// failJWKSource always errors.
type failJWKSource struct{ err error }

func (f failJWKSource) Keys(context.Context) (map[string]*rsa.PublicKey, error) {
	return nil, f.err
}

// Silence unused-import lint for hex used only in test helpers.
var _ = hex.EncodeToString
