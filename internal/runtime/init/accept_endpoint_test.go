//go:build integration

package init_test

// Purpose: the recorded-fixture provider endpoint P1-E16-W4-S35-T5's
//   scenario D drives `cascade provider add` against. Split from
//   accept_provider_test.go for the 300-line cap.
// Constraints: the responses are recorded (provenance in
//   testdata/accept/README.md); the REQUESTS are real and are asserted
//   on, so a change to what cascade sends fails the scenarios. Unrecorded
//   requests get a strict 404 -- a permissive fixture accepts a request
//   cascade should never have sent, and then the scenario passes against
//   a driver talking to the wrong endpoint.
// SPORT: internal/runtime/init acceptance (ADD) -- P1-E16-W4-S35-T5.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// endpointShape selects which vendor's recorded responses the fixture
// server replays.
type endpointShape int

const (
	// shapeAnthropic answers GET /v1/models and POST /v1/messages with an
	// x-api-key credential.
	shapeAnthropic endpointShape = iota
	// shapeOpenAICompat answers GET /v1/models and
	// POST /v1/chat/completions with a bearer token.
	shapeOpenAICompat
)

// verifyEndpoint replays the recorded provider responses and records what
// cascade sent.
type verifyEndpoint struct {
	*httptest.Server
	shape    endpointShape
	validKey string

	mu   sync.Mutex
	seen []string
	key  string
}

// newVerifyEndpoint starts the fixture server. It accepts only validKey;
// anything else gets the recorded 401.
func newVerifyEndpoint(t *testing.T, shape endpointShape, validKey string) *verifyEndpoint {
	t.Helper()
	e := &verifyEndpoint{shape: shape, validKey: validKey}
	e.Server = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.Close)
	return e
}

func (e *verifyEndpoint) serve(w http.ResponseWriter, r *http.Request) {
	e.record(r)
	w.Header().Set("content-type", "application/json")

	models, completion, ok401, okBody := e.files()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		_, _ = w.Write(fixture(nil, models))
	case r.Method == http.MethodPost && r.URL.Path == completion:
		if e.credentialOf(r) != e.validKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write(fixture(nil, ok401))
			return
		}
		_, _ = w.Write(fixture(nil, okBody))
	default:
		// A strict 404, deliberately: a permissive fixture accepts a
		// request cascade should never have sent, and then the scenario
		// passes against a driver talking to the wrong endpoint. It is
		// how the ignored `kind` field below was found.
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"no recorded response for this request"}}`))
	}
}

// files names this shape's four recorded responses and its completion path.
func (e *verifyEndpoint) files() (models, completionPath, unauthorized, success string) {
	if e.shape == shapeOpenAICompat {
		return "models.json", "/v1/chat/completions", "completion-401.json", "completion-ok.json"
	}
	return "anthropic-models.json", "/v1/messages", "anthropic-401.json", "anthropic-message-ok.json"
}

// credentialOf extracts the credential from whichever header this shape
// carries it in.
func (e *verifyEndpoint) credentialOf(r *http.Request) string {
	if e.shape == shapeOpenAICompat {
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	return r.Header.Get("x-api-key")
}

func (e *verifyEndpoint) record(r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seen = append(e.seen, r.URL.Path)
	if k := e.credentialOf(r); k != "" {
		e.key = k
	}
}

func (e *verifyEndpoint) paths() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.seen...)
}

func (e *verifyEndpoint) lastKey() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.key
}

// fixture reads one recorded response.
func fixture(t *testing.T, name string) []byte {
	raw, err := os.ReadFile(filepath.Join("testdata", "accept", "provider-verify", name))
	if err != nil {
		if t != nil {
			t.Fatalf("reading fixture %s: %v", name, err)
		}
		return []byte(`{"error":{"message":"fixture missing: ` + name + `"}}`)
	}
	return raw
}
