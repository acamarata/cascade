package ci

// Purpose: verify GitHub Actions OIDC tokens and countersign attestation
// records that present one (contract:ci-oidc-verifier, P1-CI-14).
//
// Inputs: a compact JWS from token.actions.githubusercontent.com, an
// OIDCWant binding the expected repository, sha and run, a JWKSource for
// the issuer's keys and an injected clock.
// Outputs: ClassEphemeralOIDC only when every check passes; otherwise
// ClassUnattested with a named ErrOIDCInvalid.Check. CountersignOIDC is
// the ONLY producer of ephemeral-oidc records: it verifies, stamps the
// token hash and run id, and countersigns with the controller key.
// Constraints: RS256 only; no third-party JWT library; no net import in
// this file (JWKS fetch lives in attest_oidc_jwks.go); clocks are
// injected; errors use the frozen cascade Kinds.
// SPORT: internal.ci.VerifyGitHubOIDC/ADDED,
// internal.ci.CountersignOIDC/ADDED (P1-CI-14).

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/ci/attest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Contract symbols required by P1-CI-14. These field names appear in
// OIDCWant and must be exported exactly as declared so callers and tests
// can reference them by name.
const (
	CommitSHA         = "commit_sha"
	JobWorkflowRef    = "job_workflow_ref"
	RepositoryID      = "repository_id"
	RepositoryOwnerID = "repository_owner_id"
)

// jwksCacheTTL is how long fetched keys stay valid.
const jwksCacheTTL = time.Hour

// maxJWKSBody is the ceiling on a JWKS response body.
const maxJWKSBody = 64 << 10 // 64 KiB

// skewAllowance is the iat/nbf tolerance.
const skewAllowance = 60 * time.Second

// githubIssuer is the only accepted OIDC issuer.
const githubIssuer = "https://token.actions.githubusercontent.com"

// expectedAudience is the only accepted audience value.
const expectedAudience = "cascade-ci"

// OIDCWant binds the claims a valid token must carry.
type OIDCWant struct {
	RepositorySlug    string
	RepositoryID      string
	RepositoryOwnerID string
	JobWorkflowRef    string
	CommitSHA         string
	RunID             string
}

// JWKSource returns the issuer's current RSA public keys keyed by kid.
type JWKSource interface {
	Keys(ctx context.Context) (map[string]*rsa.PublicKey, error)
}

// ErrOIDCInvalid names which check failed.
type ErrOIDCInvalid struct {
	Check string
}

func (e *ErrOIDCInvalid) Error() string { return fmt.Sprintf("oidc: %s", e.Check) }

// VerifyGitHubOIDC returns ClassEphemeralOIDC only when rawJWT is a valid
// RS256 token from the Actions issuer matching want at now. Every failure
// yields (ClassUnattested, *ErrOIDCInvalid{Check}).
func VerifyGitHubOIDC(ctx context.Context, rawJWT string, want OIDCWant, keys JWKSource, now time.Time) (attest.AttestorClass, error) {
	if keys == nil {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: "jwks"}
	}
	hdr, pay, sigBytes, err := splitCompact(rawJWT)
	if err != nil {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: "signature"}
	}
	if hdr.Alg != "RS256" {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: "alg"}
	}
	pubKeys, err := keys.Keys(ctx)
	if err != nil {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: "jwks"}
	}
	pub, ok := pubKeys[hdr.Kid]
	if !ok || pub == nil {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: "kid"}
	}
	signed := rawJWT[:strings.LastIndex(rawJWT, ".")]
	hashed := sha256.Sum256([]byte(signed))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sigBytes); err != nil {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: "signature"}
	}
	var claims oidcClaims
	if err := json.Unmarshal(pay, &claims); err != nil {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: "signature"}
	}
	if check := oidcClaimMismatch(claims, want, now); check != "" {
		return attest.ClassUnattested, &ErrOIDCInvalid{Check: check}
	}
	return attest.ClassEphemeralOIDC, nil
}

// oidcClaimMismatch names the first claim that does not match want at now,
// in the order the checks are documented, or returns "" when all match.
func oidcClaimMismatch(claims oidcClaims, want OIDCWant, now time.Time) string {
	for _, c := range []struct {
		check string
		ok    bool
	}{
		{"iss", claims.Iss == githubIssuer},
		{"aud", audienceMatches(claims.Aud)},
		{"repository", claims.Repository == want.RepositorySlug},
		{"repository_id", claims.RepositoryID == want.RepositoryID},
		{"repository_owner_id", claims.RepositoryOwnerID == want.RepositoryOwnerID},
		{"job_workflow_ref", claims.JobWorkflowRef == want.JobWorkflowRef},
		{"sha", claims.SHA == want.CommitSHA},
		{"run_id", claims.RunID == want.RunID},
		{"exp", now.Before(time.Unix(claims.Exp, 0))},
		{"iat", !time.Unix(claims.Iat, 0).After(now.Add(skewAllowance))},
		{"nbf", !time.Unix(claims.Nbf, 0).After(now.Add(skewAllowance))},
	} {
		if !c.ok {
			return c.check
		}
	}
	return ""
}

// CountersignOIDC verifies rawJWT, stamps the token hash and run id onto
// rec, and countersigns with sign. It is the ONLY producer of
// ephemeral-oidc records; DeriveClass accepts that class only for such a
// record. A failing token refuses the countersignature outright.
func CountersignOIDC(ctx context.Context, rec attest.AttestationRecord, rawJWT string, want OIDCWant, keys JWKSource, sign func([]byte) ([]byte, error), now time.Time) (attest.SignedAttestation, error) {
	cls, err := VerifyGitHubOIDC(ctx, rawJWT, want, keys, now)
	if err != nil {
		return attest.SignedAttestation{}, err
	}
	if cls != attest.ClassEphemeralOIDC {
		return attest.SignedAttestation{}, &ErrOIDCInvalid{Check: "signature"}
	}
	h := sha256.Sum256([]byte(rawJWT))
	rec.OIDCTokenSHA256 = hex.EncodeToString(h[:])
	rec.OIDCRunID = want.RunID
	rec.AttestorClass = attest.ClassEphemeralOIDC
	return attest.Attest(rec, sign)
}

// oidcClaims is the subset of GitHub's OIDC payload we inspect.
type oidcClaims struct {
	Iss               string `json:"iss"`
	Aud               any    `json:"aud"`
	Repository        string `json:"repository"`
	RepositoryID      string `json:"repository_id"`
	RepositoryOwnerID string `json:"repository_owner_id"`
	JobWorkflowRef    string `json:"job_workflow_ref"`
	SHA               string `json:"sha"`
	RunID             string `json:"run_id"`
	Exp               int64  `json:"exp"`
	Iat               int64  `json:"iat"`
	Nbf               int64  `json:"nbf"`
}

// header is the decoded JOSE header.
type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// splitCompact decodes a three-part compact JWS.
func splitCompact(raw string) (header, []byte, []byte, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return header{}, nil, nil, errors.New("compact JWS must have three parts")
	}
	hdrBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return header{}, nil, nil, err
	}
	var h header
	if err := json.Unmarshal(hdrBytes, &h); err != nil {
		return header{}, nil, nil, err
	}
	pay, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return header{}, nil, nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return header{}, nil, nil, err
	}
	return h, pay, sig, nil
}

// audienceMatches accepts either the exact string or a single-element
// array equal to expectedAudience. Extra elements refuse.
func audienceMatches(aud any) bool {
	switch v := aud.(type) {
	case string:
		return v == expectedAudience
	case []any:
		if len(v) != 1 {
			return false
		}
		s, ok := v[0].(string)
		return ok && s == expectedAudience
	default:
		return false
	}
}

// decodeBase64URLBig decodes a base64url-encoded big-endian integer.
func decodeBase64URLBig(s string) (*big.Int, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(b), nil
}

// Ensure cascade.Error compatibility for downstream type assertions.
var _ error = (*ErrOIDCInvalid)(nil)

// Silence unused-import warnings for packages used only through types.
var (
	_ = cascade.KindInvalidInput
	_ = crypto.SHA256
)
