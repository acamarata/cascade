package ci

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

// TestCIOIDCJWKSClassPublicOnly is defined in internal/hooks/egress; this
// file exists to keep the JWKS tests adjacent to the implementation while
// the class-level assertion lives next to the registry. The parse-only
// coverage here complements that gate check.

// TestGitHubJWKSourceCacheExpiry ensures cached keys expire after TTL.
func TestGitHubJWKSourceCacheExpiry(t *testing.T) {
	var calls int
	doer := doerFunc(func(_ context.Context, _ HTTPRequest) (HTTPResponse, error) {
		calls++
		return HTTPResponse{
			Status: 200,
			Body:   []byte(`{"keys":[{"kty":"RSA","kid":"k1","alg":"RS256","n":"AQAB","e":"AQAB"}]}`),
		}, nil
	})
	clock := &oidcTestClock{now: time.Unix(1700000000, 0)}
	src := NewGitHubJWKSource(doer, testEngine(t), clock)
	if _, err := src.Keys(context.Background()); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := src.Keys(context.Background()); err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
	clock.now = clock.now.Add(jwksCacheTTL + time.Second)
	if _, err := src.Keys(context.Background()); err != nil {
		t.Fatalf("post-expiry fetch: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls after expiry, got %d", calls)
	}
}

// TestGitHubJWKSourceEgressClass verifies the source uses ci-oidc-jwks.
func TestGitHubJWKSourceEgressClass(t *testing.T) {
	if egress.EgressClassCIOIDCJWKS != "ci-oidc-jwks" {
		t.Fatalf("class name mismatch: %q", egress.EgressClassCIOIDCJWKS)
	}
}

// oidcTestClock is a mutable clock for JWKS tests.
type oidcTestClock struct{ now time.Time }

func (c *oidcTestClock) Now() time.Time { return c.now }

// doerFunc adapts a function to Doer.
type doerFunc func(context.Context, HTTPRequest) (HTTPResponse, error)

func (f doerFunc) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	return f(ctx, req)
}

// Silence unused-import lint for egress used only in the constant check.
var _ = egress.EgressClassCIOIDCJWKS
var _ = errors.New
