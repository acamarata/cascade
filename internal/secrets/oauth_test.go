// Purpose: the OAuth broker's test doubles and its authorization-flow
//
//	tests. Every double here stands in for a TRANSPORT, never for the
//	protocol logic under test: the mock IdP performs the real RFC 7636
//	§4.6 verifier check, so "PKCE is implemented" is proven by a server
//	that rejects a mismatched verifier rather than asserted by the client
//	that sends one.
//
// Constraints: no "net" or "net/http" import - the default unit lane
//
//	forbids it (internal/build's no-network gate), and the whole point of
//	the seam split is that these rules need no socket. The socket half
//	lives in oauth_transport_test.go behind the integration tag.
//
// SPORT: OAUTH_BROKER: ADD (tests).

package secrets

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// The canaries. Every assertion about "no token reaches an output" looks
// for these exact strings, the way the vault's own canary tests do.
const (
	canaryAccess  = "CANARY-ACCESS-TOKEN-b4f0d1e2"
	canaryRefresh = "CANARY-REFRESH-TOKEN-9a7c3b55"
	canaryCode    = "CANARY-AUTH-CODE-0d2e6f81"
)

// testOAuthConfig is a valid configuration pointing at a loopback IdP.
func testOAuthConfig() provider.ProviderOAuthConfig {
	return provider.ProviderOAuthConfig{
		ProviderID:    "testidp",
		ClientID:      "client-123",
		Scopes:        []string{"read", "write"},
		RedirectURI:   "http://127.0.0.1/callback",
		AuthEndpoint:  "https://idp.example/authorize",
		TokenEndpoint: "https://idp.example/token",
		PKCEMethod:    provider.PKCEMethodS256,
	}
}

// mockIDP is a token endpoint that enforces PKCE. It records the challenge
// the client sent at authorization time and refuses an exchange whose
// code_verifier does not hash to it - which is what makes the
// mismatched-verifier test a real rejection rather than a stubbed one.
type mockIDP struct {
	mu        sync.Mutex
	challenge string
	calls     int
	forms     []url.Values
	// body, when set, replaces the success response.
	body string
	// status, when non-zero, replaces the success status.
	status int
	// failWith, when set, is returned instead of any response.
	failWith error
}

func (m *mockIDP) Exchange(_ context.Context, _ string, form url.Values) ([]byte, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.forms = append(m.forms, form)
	if m.failWith != nil {
		return nil, 0, m.failWith
	}
	if v := form.Get("code_verifier"); v != "" && !pkceVerify([]byte(v), m.challenge) {
		return []byte(`{"error":"invalid_grant"}`), 400, nil
	}
	if m.body != "" || m.status != 0 {
		status := m.status
		if status == 0 {
			status = 200
		}
		return []byte(m.body), status, nil
	}
	return []byte(`{"access_token":"` + canaryAccess + `","refresh_token":"` + canaryRefresh +
		`","token_type":"Bearer","expires_in":3600,"scope":"read write"}`), 200, nil
}

func (m *mockIDP) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// stubListener is a bound-port stand-in. It hands back one prepared query
// string, or blocks until ctx ends when none is prepared.
type stubListener struct {
	port string
	// resolve produces the redirect query at Wait time, not at bind time:
	// the broker mints its state AFTER binding the port, so a query
	// captured earlier could not carry the real state.
	resolve func() string
	block   bool
	closes  int
}

func (l *stubListener) Port() string { return l.port }

func (l *stubListener) Wait(ctx context.Context) (string, error) {
	if l.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return l.resolve(), nil
}

func (l *stubListener) Close() error { l.closes++; return nil }

// oauthHarness bundles everything a flow test needs.
type oauthHarness struct {
	broker   *OAuthBroker
	idp      *mockIDP
	listener *stubListener
	vault    *Broker
	custody  *memCustody
	diag     *strings.Builder
	opened   []string
	now      time.Time
	// lastQuery is the redirect query the stub listener last handed back,
	// so a replay test can present the exact same state a second time.
	lastQuery string
}

// newOAuthHarness wires a broker over the in-memory custody with stub
// transports. callbackQuery is what the "browser" redirects with; the {{state}}
// placeholder is replaced with the real state the broker minted.
func newOAuthHarness(t *testing.T, cfg provider.ProviderOAuthConfig, callbackQuery string) *oauthHarness {
	t.Helper()
	vault, custody := newTestBroker(t, &allowGate{})
	h := &oauthHarness{
		idp:      &mockIDP{},
		listener: &stubListener{port: "54321"},
		vault:    vault,
		custody:  custody,
		diag:     &strings.Builder{},
		now:      time.Unix(1_700_000_000, 0).UTC(),
	}
	h.listener.resolve = func() string {
		h.lastQuery = h.expand(callbackQuery)
		return h.lastQuery
	}
	listen := func(context.Context) (callbackListener, error) { return h.listener, nil }
	open := func(_ context.Context, rawURL string) error {
		h.opened = append(h.opened, rawURL)
		return nil
	}
	broker, err := newOAuthBroker(cfg, OAuthDeps{
		Vault: vault, Clock: fixedClock{at: h.now}, Diagnostics: h.diag,
		LookupEnv: func(string) (string, bool) { return "", false },
	}, h.idp, listen, open)
	if err != nil {
		t.Fatalf("newOAuthBroker: %v", err)
	}
	h.broker = broker
	return h
}

// expand substitutes the live state into a callback template, and teaches
// the mock IdP the challenge this authorization published. The broker mints
// both inside beginSession, so the stub listener resolves the template
// lazily, after the session exists - which also models reality: the IdP
// learns the challenge from the authorization request, not from cascade.
func (h *oauthHarness) expand(template string) string {
	h.broker.mu.Lock()
	defer h.broker.mu.Unlock()
	for state, session := range h.broker.sessions {
		h.idp.mu.Lock()
		h.idp.challenge = session.codes.challenge
		h.idp.mu.Unlock()
		return strings.ReplaceAll(template, "{{state}}", url.QueryEscape(state))
	}
	return strings.ReplaceAll(template, "{{state}}", "no-session-yet")
}

func TestOAuthStartStoresTokensAsVaultRefs(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	rec, err := h.broker.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rec.AccessRef == "" || rec.RefreshRef == "" {
		t.Fatalf("the record names no vault refs: %+v", rec)
	}
	if got := string(h.custody.entries[rec.AccessRef]); got != canaryAccess {
		t.Fatalf("the access token did not reach the vault under its ref: %q", got)
	}
	if got := string(h.custody.entries[rec.RefreshRef]); got != canaryRefresh {
		t.Fatalf("the refresh token did not reach the vault under its ref: %q", got)
	}
	want := h.now.Add(3600 * time.Second)
	if !rec.ExpiresAt.Equal(want) {
		t.Fatalf("expiry %v, want %v (expires_in applied to the injected clock)", rec.ExpiresAt, want)
	}
	if len(rec.Scopes) != 2 {
		t.Fatalf("granted scopes not recorded: %+v", rec.Scopes)
	}
}

func TestOAuthStartSendsS256ChallengeNotTheVerifier(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	if _, err := h.broker.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(h.opened) != 1 {
		t.Fatalf("expected one browser launch, got %d", len(h.opened))
	}
	q, err := url.Parse(h.opened[0])
	if err != nil {
		t.Fatalf("parsing the authorization URL: %v", err)
	}
	values := q.Query()
	if values.Get("code_challenge_method") != "S256" {
		t.Fatalf("challenge method %q, want S256", values.Get("code_challenge_method"))
	}
	verifier := h.idp.forms[0].Get("code_verifier")
	if verifier == "" {
		t.Fatal("no code_verifier was sent at exchange time")
	}
	if values.Get("code_challenge") == verifier {
		t.Fatal("the verifier itself was sent as the challenge; that is the plain method, which is forbidden")
	}
	if !pkceVerify([]byte(verifier), values.Get("code_challenge")) {
		t.Fatal("the sent challenge is not the S256 transform of the sent verifier")
	}
	if strings.Contains(h.opened[0], verifier) {
		t.Fatal("the verifier appears in the authorization URL")
	}
}

func TestOAuthVerifierIsFreshPerAuthorization(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
		if _, err := h.broker.Start(context.Background()); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		verifier := h.idp.forms[0].Get("code_verifier")
		if seen[verifier] {
			t.Fatal("two authorizations shared one PKCE verifier")
		}
		seen[verifier] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected 4 distinct verifiers, got %d", len(seen))
	}
}

func TestOAuthMismatchedVerifierIsRejected(t *testing.T) {
	h := newOAuthHarness(t, testOAuthConfig(), "code="+canaryCode+"&state={{state}}")
	// The IdP holds the challenge from a DIFFERENT authorization, so the
	// verifier this flow presents cannot hash to it. The IdP - not the
	// client - is what rejects the exchange, which is the real contract.
	resolve := h.listener.resolve
	h.listener.resolve = func() string {
		query := resolve()
		h.idp.mu.Lock()
		defer h.idp.mu.Unlock()
		h.idp.challenge = pkceChallenge([]byte("some-other-authorizations-verifier"))
		return query
	}
	_, err := h.broker.Start(context.Background())
	if !errors.Is(err, ErrPKCEMismatch) {
		t.Fatalf("a mismatched verifier was not rejected: %v", err)
	}
}
