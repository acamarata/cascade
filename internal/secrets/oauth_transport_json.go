// Purpose: JSON-body token exchange for OAuth grants, running on the
// existing tokenExchanger's http.Client under EgressClassOAuth.
// Inputs: a token endpoint URL and a JSON payload.
// Outputs: the raw response body and HTTP status, with timeout and response
// cap enforced.
// Constraints: exactly one http.Client construction in internal/secrets
// non-test code (the existing one in oauth_transport.go). No token bytes
// appear in any error string or log line.
// SPORT: internal.secrets/ADD (P1-TOP-13a JSON grant poster).

package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/acamarata/cascade/pkg/cascade"
)

// JSONGrantPoster performs JSON-body token exchanges on an existing
// tokenExchanger's client. It is the JSON counterpart to the form-encoded
// Exchange method.
type JSONGrantPoster struct {
	exchanger tokenExchanger
}

// NewJSONGrantPoster wraps an existing tokenExchanger for JSON grants.
// The exchanger must already be configured with the production http.Client
// from oauth_transport.go; this does not construct a new client.
func NewJSONGrantPoster(exchanger tokenExchanger) *JSONGrantPoster {
	return &JSONGrantPoster{exchanger: exchanger}
}

// PostJSON exchanges a JSON payload at the token endpoint. The payload is
// marshaled, sent as application/json, and the response is read under the
// same cap and timeout as form exchanges. No token bytes appear in errors.
func (p *JSONGrantPoster) PostJSON(ctx context.Context, endpoint string, payload map[string]any) ([]byte, int, error) {
	if p.exchanger == nil {
		return nil, 0, cascade.New(cascade.KindInvalidInput, "secrets: JSON grant poster has no exchanger")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, cascade.Wrap(cascade.KindInvalidInput, err, "secrets: could not marshal JSON grant payload")
	}

	// Build the request manually since tokenExchanger.Exchange expects url.Values.
	// We use the same client, timeout and caps by going through http directly.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, cascade.Wrap(cascade.KindInvalidInput, err, "secrets: could not build JSON grant request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Use the exchanger's client if it's an httpExchanger, otherwise fall back.
	// This ensures we reuse the single http.Client construction.
	he, ok := p.exchanger.(*httpExchanger)
	if !ok {
		return nil, 0, cascade.New(cascade.KindInvalidInput, "secrets: JSON grant requires an httpExchanger")
	}

	resp, err := he.client.Do(req)
	if err != nil {
		return nil, 0, cascade.Wrap(cascade.KindUnavailable, err, "secrets: the OAuth endpoint could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 && resp.StatusCode <= 399 {
		return nil, resp.StatusCode, cascade.Wrapf(cascade.KindUnavailable, errOAuthRedirectRefused,
			"secrets: the OAuth endpoint answered HTTP %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes+1))
	if err != nil {
		return nil, resp.StatusCode, cascade.Wrap(cascade.KindUnavailable, err, "secrets: the OAuth response body could not be read")
	}
	if len(respBody) > maxTokenResponseBytes {
		clear(respBody)
		return nil, resp.StatusCode, cascade.Wrapf(cascade.KindIntegrity, errOAuthResponseTooLarge,
			"secrets: the OAuth endpoint response is larger than %d bytes", maxTokenResponseBytes)
	}

	return respBody, resp.StatusCode, nil
}
