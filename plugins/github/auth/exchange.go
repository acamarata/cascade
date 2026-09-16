package auth

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): redeem an authorization code for a token, and renew
//
//	one after a 401.
//
// Inputs: the PKCE verifier, the code the callback carried, and the
//
//	redirect URI the flow was started with.
//
// Outputs: a validated TokenResponse.
// Constraints: the exchange carries NO client secret — that is the whole
//
//	point of PKCE for a public client, and a secret shipped in a plugin
//	binary would be a secret in every user's filesystem. The verifier is
//	what proves this process started the flow.
//
//	net/http lives here and in loopback.go only (Art.7.2 forbids an
//	untagged _test.go from importing it), so the seam this package's unit
//	tests use is Poster: a function typed in plain values.
//
// SPORT: plugins/github/auth:exchange (ADD) — P1-E25-W5-S51-T1.

// ExchangeTimeout bounds a token-endpoint call.
const ExchangeTimeout = 30 * time.Second

// Poster performs one form POST to the token endpoint and returns the
// status and body. It is the seam that keeps Exchange testable without a
// socket.
type Poster func(ctx context.Context, endpoint string, form url.Values) (status int, body []byte, err error)

// Exchange redeems code for a token.
//
// The response is validated before it is returned: GitHub answers a failed
// exchange with HTTP 200 and an `error` member rather than an error status,
// so a caller checking only the status code would accept a failure as a
// token. TokenResponse.Validate is what catches that, and it is why this
// returns through it rather than around it.
func Exchange(ctx context.Context, post Poster, cfg provider.ProviderOAuthConfig,
	code, verifier, redirectURI string,
) (TokenResponse, error) {
	if post == nil {
		return TokenResponse{}, cascade.New(cascade.KindInternal, "github: no token-endpoint transport")
	}
	if strings.TrimSpace(code) == "" {
		return TokenResponse{}, cascade.New(cascade.KindInvalidInput, "github: no authorization code to redeem")
	}
	if strings.TrimSpace(verifier) == "" {
		return TokenResponse{}, cascade.New(cascade.KindInvalidInput, "github: no PKCE verifier to prove the flow")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cfg.ClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}

	status, body, err := post(ctx, TokenEndpoint, form)
	if err != nil {
		return TokenResponse{}, cascade.Wrap(cascade.KindUnavailable, err, "github: redeeming the authorization code")
	}
	if status < 200 || status > 299 {
		return TokenResponse{}, cascade.Newf(cascade.KindPermissionDenied,
			"github: the token endpoint refused the exchange with %d", status)
	}

	var out TokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return TokenResponse{}, cascade.Wrap(cascade.KindIntegrity, err, "github: decoding the token response")
	}
	if err := out.Validate(); err != nil {
		return TokenResponse{}, err
	}
	return out, nil
}
