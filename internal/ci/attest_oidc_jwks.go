package ci

// Purpose: fetch and cache GitHub Actions' OIDC JWKS through the
// ci-oidc-jwks egress class (contract:ci-oidc-verifier, P1-CI-14).
//
// Inputs: a Doer for outbound HTTP, an *egress.Engine to gate the call
// at public tier, and an injected clock for cache expiry.
// Outputs: a JWKSource whose Keys method returns the current RSA keys
// keyed by kid, or ErrOIDCInvalid{Check:"jwks"} on any failure.
// Constraints: body cap 64 KiB; 1 h in-memory cache; no net import in
// this package (the Doer is injected); RS256 keys only.
// SPORT: internal.ci.NewGitHubJWKSource/ADDED (P1-CI-14).

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

// jwksURL is the Actions issuer's JWKS endpoint.
const jwksURL = "https://token.actions.githubusercontent.com/.well-known/jwks"

// githubJWKSource caches the fetched key set for jwksCacheTTL.
type githubJWKSource struct {
	doer   Doer
	engine *egress.Engine
	clock  Clock

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// NewGitHubJWKSource builds a JWKSource that fetches through the
// ci-oidc-jwks egress class at public tier, caps the body at 64 KiB and
// caches keys for one hour. Any fetch, status or parse failure surfaces
// as ErrOIDCInvalid{Check:"jwks"}.
func NewGitHubJWKSource(doer Doer, engine *egress.Engine, clock Clock) JWKSource {
	return &githubJWKSource{doer: doer, engine: engine, clock: clock}
}

// Keys returns the cached key set, fetching a fresh copy when the cache
// is missing or older than jwksCacheTTL.
func (s *githubJWKSource) Keys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	if s.keys != nil && now.Sub(s.fetched) < jwksCacheTTL {
		return s.keys, nil
	}
	keys, err := s.fetchKeysLocked(ctx)
	if err != nil {
		return nil, err
	}
	s.keys = keys
	s.fetched = now
	return keys, nil
}

// fetchKeysLocked performs the gated fetch and parse. Caller holds mu.
func (s *githubJWKSource) fetchKeysLocked(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	if s.engine == nil {
		return nil, &ErrOIDCInvalid{Check: "jwks"}
	}
	if _, err := s.engine.InterceptClass(ctx, egress.EgressClassCIOIDCJWKS, egress.TierPublic, nil); err != nil {
		return nil, &ErrOIDCInvalid{Check: "jwks"}
	}
	resp, err := s.doer.Do(ctx, HTTPRequest{Method: "GET", URL: jwksURL, Headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		return nil, &ErrOIDCInvalid{Check: "jwks"}
	}
	if resp.Status != 200 {
		return nil, &ErrOIDCInvalid{Check: "jwks"}
	}
	if len(resp.Body) > maxJWKSBody {
		return nil, &ErrOIDCInvalid{Check: "jwks"}
	}
	keys, err := ParseJWKS(resp.Body)
	if err != nil {
		return nil, &ErrOIDCInvalid{Check: "jwks"}
	}
	return keys, nil
}

// jwksDocument is the top-level JWKS shape.
type jwksDocument struct {
	Keys []jwkEntry `json:"keys"`
}

// jwkEntry is one key in the set. Only RSA keys are retained.
type jwkEntry struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// ParseJWKS decodes a JWKS body into RSA public keys keyed by kid. Non-RSA
// entries are skipped; a document with no usable keys is an error.
func ParseJWKS(body []byte) (map[string]*rsa.PublicKey, error) {
	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Kid == "" || k.N == "" || k.E == "" {
			continue
		}
		n, err := decodeBase64URLBig(k.N)
		if err != nil {
			return nil, fmt.Errorf("jwks: kid %s n: %w", k.Kid, err)
		}
		e, err := decodeBase64URLBig(k.E)
		if err != nil {
			return nil, fmt.Errorf("jwks: kid %s e: %w", k.Kid, err)
		}
		if !e.IsInt64() {
			return nil, errors.New("jwks: exponent too large")
		}
		out[k.Kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
	}
	if len(out) == 0 {
		return nil, errors.New("jwks: no RSA keys")
	}
	return out, nil
}

// Silence unused-import lint for big.Int usage through decodeBase64URLBig.
var _ = (*big.Int)(nil)
