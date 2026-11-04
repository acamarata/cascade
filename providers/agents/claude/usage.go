// Purpose: fetch usage windows and build refresh grant requests for the
// Claude provider. Both endpoints live in UsageDescriptor; no literal host
// appears in this file.
// Inputs: a UsageDescriptor (which carries the host), a bearer token or
// refresh token, and an injected Doer for the GET; or a refresh token and
// clock for the refresh request shape.
// Outputs: a UsageDocument parsed from the response, or a Request shaped
// for the token endpoint.
// Constraints: a 5xx, timeout or malformed body returns an error, never a
// zero document. No token bytes appear in any error string or log line.
// SPORT: providers.agents.claude/ADD (P1-TOP-13a usage endpoints).

package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// UsageDescriptor carries the host and path for usage and refresh endpoints.
type UsageDescriptor struct {
	// Host is the base URL (e.g., "https://api.anthropic.com").
	Host string
	// UsagePath is the GET path (e.g., "/v1/usage").
	UsagePath string
	// RefreshPath is the POST path (e.g., "/v1/oauth/token").
	RefreshPath string
}

// UsageWindow is one rolling window in the usage document.
type UsageWindow struct {
	// DurationHours is the window length (5 = 5h, 168 = 7d).
	DurationHours int `json:"duration_hours"`
	// TokensUsed is the count consumed in this window.
	TokensUsed int64 `json:"tokens_used"`
	// TokensLimit is the cap for this window.
	TokensLimit int64 `json:"tokens_limit"`
	// ResetsAt is when this window resets.
	ResetsAt time.Time `json:"resets_at"`
}

// UsageDocument holds the parsed usage response.
type UsageDocument struct {
	// Windows is the list of rolling windows (typically 5h and 7d).
	Windows []UsageWindow `json:"windows"`
}

// Doer performs HTTP requests. It is injected so tests need no network.
type Doer interface {
	Do(ctx context.Context, method, url string, body []byte, headers map[string]string) ([]byte, int, error)
}

// Request is a shaped HTTP request ready to send.
type Request struct {
	// Method is GET, POST, etc.
	Method string
	// URL is the full endpoint.
	URL string
	// Body is the request body (JSON-encoded).
	Body []byte
	// Headers are the request headers.
	Headers map[string]string
}

// userAgentVersion is the User-Agent string used in refresh requests.
const userAgentVersion = "cascade/0.1.0"

// FetchUsage GETs the usage document from the descriptor's host. A 5xx,
// timeout or malformed body returns an error; a zero document is never
// returned on error. The token is sent as a Bearer header and never logged.
func FetchUsage(ctx context.Context, d UsageDescriptor, token []byte, doer Doer) (UsageDocument, error) {
	if doer == nil {
		return UsageDocument{}, cascade.New(cascade.KindInvalidInput, "claude: usage doer is nil")
	}
	if d.Host == "" || d.UsagePath == "" {
		return UsageDocument{}, cascade.New(cascade.KindInvalidInput, "claude: usage descriptor missing host or path")
	}
	url := d.Host + d.UsagePath
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", string(token)),
		"Accept":        "application/json",
	}
	body, status, err := doer.Do(ctx, "GET", url, nil, headers)
	if err != nil {
		return UsageDocument{}, cascade.Wrap(cascade.KindUnavailable, err, "claude: usage GET failed")
	}
	if status >= 500 {
		return UsageDocument{}, cascade.Newf(cascade.KindUnavailable, "claude: usage endpoint returned HTTP %d", status)
	}
	if status < 200 || status >= 300 {
		return UsageDocument{}, cascade.Newf(cascade.KindUnavailable, "claude: usage endpoint returned HTTP %d", status)
	}
	var doc UsageDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return UsageDocument{}, cascade.Wrap(cascade.KindInvalidInput, err, "claude: usage response is not valid JSON")
	}
	if len(doc.Windows) == 0 {
		return UsageDocument{}, cascade.New(cascade.KindInvalidInput, "claude: usage response has no windows")
	}
	return doc, nil
}

// RefreshRequest builds a POST request for the refresh grant. The request
// shape includes the grant_type, refresh_token, client_id, timestamp and
// user_agent. The token is never logged or echoed in errors.
func RefreshRequest(d UsageDescriptor, refresh []byte, now time.Time) (Request, error) {
	if d.Host == "" || d.RefreshPath == "" {
		return Request{}, cascade.New(cascade.KindInvalidInput, "claude: refresh descriptor missing host or path")
	}
	if len(refresh) == 0 {
		return Request{}, cascade.New(cascade.KindInvalidInput, "claude: refresh token is empty")
	}
	url := d.Host + d.RefreshPath
	payload := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": string(refresh),
		"client_id":     "cascade-client",
		"timestamp":     now.UTC().Format(time.RFC3339),
		"user_agent":    userAgentVersion,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Request{}, cascade.Wrap(cascade.KindInvalidInput, err, "claude: could not marshal refresh request")
	}
	headers := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		"User-Agent":   userAgentVersion,
	}
	return Request{
		Method:  "POST",
		URL:     url,
		Body:    body,
		Headers: headers,
	}, nil
}
