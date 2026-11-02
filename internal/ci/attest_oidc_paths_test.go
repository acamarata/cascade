package ci

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/ci/attest"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// TestAttestOIDCMalformedTokensAreUnattested walks every decode step of the
// compact JWS: each malformed shape is refused as a signature failure.
func TestAttestOIDCMalformedTokensAreUnattested(t *testing.T) {
	env := newTestOIDCEnv(t)
	hdr := b64(`{"alg":"RS256","kid":"test-kid"}`)
	for name, raw := range map[string]string{
		"two parts":          hdr + "." + b64("{}"),
		"header not base64":  "!!!." + b64("{}") + "." + b64("sig"),
		"header not json":    b64("not json") + "." + b64("{}") + "." + b64("sig"),
		"payload not base64": hdr + ".!!!." + b64("sig"),
		"sig not base64":     hdr + "." + b64("{}") + ".!!!",
	} {
		t.Run(name, func(t *testing.T) { assertOIDCRejected(t, env, raw, "signature") })
	}
	t.Run("signed payload not json", func(t *testing.T) {
		signed := hdr + "." + b64("not json")
		digest := sha256.Sum256([]byte(signed))
		sig, err := rsa.SignPKCS1v15(rand.Reader, env.key, 5, digest[:]) // crypto.SHA256
		if err != nil {
			t.Fatal(err)
		}
		assertOIDCRejected(t, env, signed+"."+base64.RawURLEncoding.EncodeToString(sig), "signature")
	})
}

// TestAttestOIDCRefusesMissingKeySources covers a nil source and a source
// that lists the kid with no key.
func TestAttestOIDCRefusesMissingKeySources(t *testing.T) {
	env := newTestOIDCEnv(t)
	raw := env.signToken(t, env.validClaims(), "", "")
	cls, err := VerifyGitHubOIDC(context.Background(), raw, env.want, nil, env.now)
	var oidcErr *ErrOIDCInvalid
	if cls != attest.ClassUnattested || !errors.As(err, &oidcErr) || oidcErr.Check != "jwks" {
		t.Fatalf("nil source = %q, %v", cls, err)
	}
	env.source = staticJWKSource{"test-kid": nil}
	assertOIDCRejected(t, env, raw, "kid")
	if got := oidcErr.Error(); got != "oidc: jwks" {
		t.Fatalf("error text = %q", got)
	}
}

// TestAttestOIDCAudienceShapes accepts a one-element array and refuses every
// other shape.
func TestAttestOIDCAudienceShapes(t *testing.T) {
	env := newTestOIDCEnv(t)
	c := env.validClaims()
	c["aud"] = []any{expectedAudience}
	if cls, err := VerifyGitHubOIDC(context.Background(), env.signToken(t, c, "", ""), env.want, env.source, env.now); err != nil || cls != attest.ClassEphemeralOIDC {
		t.Fatalf("one-element audience = %q, %v", cls, err)
	}
	for name, aud := range map[string]any{
		"two elements":     []any{expectedAudience, "other"},
		"non-string":       []any{7},
		"number":           7,
		"empty array":      []any{},
		"wrong in array":   []any{"other"},
		"wrong single str": "other",
	} {
		t.Run(name, func(t *testing.T) {
			c := env.validClaims()
			c["aud"] = aud
			assertOIDCRejected(t, env, env.signToken(t, c, "", ""), "aud")
		})
	}
}

func jwksSource(t *testing.T, resp HTTPResponse, err error) JWKSource {
	t.Helper()
	doer := doerFunc(func(context.Context, HTTPRequest) (HTTPResponse, error) { return resp, err })
	return NewGitHubJWKSource(doer, testEngine(t), &oidcTestClock{})
}

// TestGitHubJWKSourceFetchFailures refuses a transport error, a non-200
// status, an oversize body and an unparsable body, each as a jwks failure.
func TestGitHubJWKSourceFetchFailures(t *testing.T) {
	for name, src := range map[string]JWKSource{
		"transport error": jwksSource(t, HTTPResponse{}, errors.New("down")),
		"status":          jwksSource(t, HTTPResponse{Status: 503}, nil),
		"oversize":        jwksSource(t, HTTPResponse{Status: 200, Body: []byte(strings.Repeat(" ", maxJWKSBody+1))}, nil),
		"unparsable":      jwksSource(t, HTTPResponse{Status: 200, Body: []byte("{")}, nil),
		"no engine":       NewGitHubJWKSource(doerFunc(nil), nil, &oidcTestClock{}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := src.Keys(context.Background())
			var oidcErr *ErrOIDCInvalid
			if !errors.As(err, &oidcErr) || oidcErr.Check != "jwks" {
				t.Fatalf("Keys error = %v, want a jwks refusal", err)
			}
		})
	}
}

// TestParseJWKSRefusals covers the malformed key documents.
func TestParseJWKSRefusals(t *testing.T) {
	n := base64.RawURLEncoding.EncodeToString(big.NewInt(65537).Bytes())
	key := func(kid, nn, e string) string {
		return `{"keys":[{"kty":"RSA","kid":"` + kid + `","n":"` + nn + `","e":"` + e + `"}]}`
	}
	huge := base64.RawURLEncoding.EncodeToString(new(big.Int).Lsh(big.NewInt(1), 80).Bytes())
	for name, body := range map[string]string{
		"not json":         "{",
		"modulus bad":      key("k", "!!!", "AQAB"),
		"exponent bad":     key("k", n, "!!!"),
		"exponent too big": key("k", n, huge),
		"no keys":          `{"keys":[]}`,
		"only non-RSA":     `{"keys":[{"kty":"EC","kid":"k","n":"AQAB","e":"AQAB"}]}`,
		"incomplete entry": `{"keys":[{"kty":"RSA","kid":"","n":"AQAB","e":"AQAB"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if keys, err := ParseJWKS([]byte(body)); err == nil {
				t.Fatalf("ParseJWKS accepted %s: %v", name, keys)
			}
		})
	}
}
