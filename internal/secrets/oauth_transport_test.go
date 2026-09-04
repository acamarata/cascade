//go:build integration

// Purpose: the socket half of the OAuth broker - the real loopback
//
//	listener, the real HTTP exchanger, and the recorded-fixture check that
//	pins what cascade actually puts on the wire.
//
// Constraints: build-tagged "integration" because it imports "net" and
//
//	"net/http", which the default unit lane forbids (internal/build's
//	no-network gate). Everything the protocol decides is already asserted
//	without a socket in oauth_test.go; what needs a real one is exactly
//	this: that a port is bound, released, and that the bytes on the wire
//	match the fixture.
//
// SPORT: OAUTH_BROKER: ADD (integration tests).

package secrets

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// pkceFixture is the recorded exchange shape.
type pkceFixture struct {
	AuthorizationRequest struct {
		RequiredQueryParams  []string `json:"required_query_params"`
		ResponseType         string   `json:"response_type"`
		CodeChallengeMethod  string   `json:"code_challenge_method"`
		ForbiddenQueryParams []string `json:"forbidden_query_params"`
	} `json:"authorization_request"`
	TokenRequest struct {
		Method              string   `json:"method"`
		ContentType         string   `json:"content_type"`
		Accept              string   `json:"accept"`
		RequiredFormParams  []string `json:"required_form_params"`
		GrantType           string   `json:"grant_type"`
		ForbiddenFormParams []string `json:"forbidden_form_params"`
	} `json:"token_request"`
	TokenResponse struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	} `json:"token_response"`
}

func loadPKCEFixture(t *testing.T) pkceFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/pkce-exchange-fixture.json")
	if err != nil {
		t.Fatalf("reading the PKCE fixture: %v", err)
	}
	var fixture pkceFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decoding the PKCE fixture: %v", err)
	}
	return fixture
}

// TestOAuthPKCEFixture runs the whole flow over real sockets against a
// conformant IdP and asserts the wire request matches the fixture.
func TestOAuthPKCEFixture(t *testing.T) {
	fixture := loadPKCEFixture(t)
	var authQuery map[string][]string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assertTokenRequestMatchesFixture(t, r, fixture)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the token request form: %v", err)
		}
		challenge := ""
		if authQuery != nil {
			challenge = authQuery["code_challenge"][0]
		}
		if !pkceVerify([]byte(r.PostForm.Get("code_verifier")), challenge) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fixture.TokenResponse.Status)
		_, _ = w.Write(fixture.TokenResponse.Body)
	}))
	defer idp.Close()

	vault, custody := newTestBroker(t, &allowGate{})
	cfg := testOAuthConfig()
	cfg.TokenEndpoint = idp.URL + "/token"
	cfg.AuthEndpoint = idp.URL + "/authorize"

	broker, err := NewOAuthBroker(cfg, OAuthDeps{Vault: vault, Clock: fixedClock{at: time.Unix(1_700_000_000, 0).UTC()}})
	if err != nil {
		t.Fatalf("NewOAuthBroker: %v", err)
	}
	// The "browser": follow the authorization URL's redirect_uri back to the
	// loopback listener, exactly as a real browser would.
	broker.open = func(ctx context.Context, rawURL string) error {
		parsed, perr := url.Parse(rawURL)
		if perr != nil {
			return perr
		}
		authQuery = parsed.Query()
		assertAuthorizationRequestMatchesFixture(t, authQuery, fixture)
		callback := parsed.Query().Get("redirect_uri") + "?code=fixture-code&state=" + parsed.Query().Get("state")
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, callback, nil)
		if rerr != nil {
			return rerr
		}
		resp, derr := http.DefaultClient.Do(req)
		if derr != nil {
			return derr
		}
		return resp.Body.Close()
	}

	rec, err := broker.Start(context.Background())
	if err != nil {
		t.Fatalf("Start over real sockets: %v", err)
	}
	if string(custody.entries[rec.AccessRef]) != "FIXTURE-ACCESS-NOT-A-REAL-TOKEN" {
		t.Fatalf("the fixture's access token did not reach the vault")
	}
	if rec.RefreshRef == "" {
		t.Fatal("the fixture's refresh token was not stored")
	}
}

func assertAuthorizationRequestMatchesFixture(t *testing.T, query map[string][]string, fixture pkceFixture) {
	t.Helper()
	for _, key := range fixture.AuthorizationRequest.RequiredQueryParams {
		if len(query[key]) != 1 || query[key][0] == "" {
			t.Errorf("the authorization request is missing %q", key)
		}
	}
	for _, key := range fixture.AuthorizationRequest.ForbiddenQueryParams {
		if len(query[key]) != 0 {
			t.Errorf("the authorization request carries %q, which the fixture forbids", key)
		}
	}
	if got := query["response_type"][0]; got != fixture.AuthorizationRequest.ResponseType {
		t.Errorf("response_type %q, fixture says %q", got, fixture.AuthorizationRequest.ResponseType)
	}
	if got := query["code_challenge_method"][0]; got != fixture.AuthorizationRequest.CodeChallengeMethod {
		t.Errorf("code_challenge_method %q, fixture says %q", got, fixture.AuthorizationRequest.CodeChallengeMethod)
	}
}

func assertTokenRequestMatchesFixture(t *testing.T, r *http.Request, fixture pkceFixture) {
	t.Helper()
	if r.Method != fixture.TokenRequest.Method {
		t.Errorf("token request method %q, fixture says %q", r.Method, fixture.TokenRequest.Method)
	}
	if ct := r.Header.Get("Content-Type"); ct != fixture.TokenRequest.ContentType {
		t.Errorf("Content-Type %q, fixture says %q", ct, fixture.TokenRequest.ContentType)
	}
	if a := r.Header.Get("Accept"); a != fixture.TokenRequest.Accept {
		t.Errorf("Accept %q, fixture says %q", a, fixture.TokenRequest.Accept)
	}
	if err := r.ParseForm(); err != nil {
		t.Fatalf("parsing the token request form: %v", err)
	}
	for _, key := range fixture.TokenRequest.RequiredFormParams {
		if r.PostForm.Get(key) == "" {
			t.Errorf("the token request is missing %q", key)
		}
	}
	for _, key := range fixture.TokenRequest.ForbiddenFormParams {
		if r.PostForm.Get(key) != "" {
			t.Errorf("the token request carries %q, which the fixture forbids", key)
		}
	}
	if got := r.PostForm.Get("grant_type"); got != fixture.TokenRequest.GrantType {
		t.Errorf("grant_type %q, fixture says %q", got, fixture.TokenRequest.GrantType)
	}
}

// TestOAuthLoopbackListenerReleasesItsPort asserts the deadline/cancel path
// leaves nothing bound: the port must be re-bindable immediately after Close.
func TestOAuthLoopbackListenerReleasesItsPort(t *testing.T) {
	listener, err := listenLoopback(context.Background())
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	port := listener.Port()
	if port == "" || port == "0" {
		t.Fatalf("no ephemeral port was bound (got %q)", port)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("Close is not idempotent: %v", err)
	}
	rebound, err := net.Listen("tcp", provider.LoopbackHost+":"+port)
	if err != nil {
		t.Fatalf("port %s is still bound after Close: %v", port, err)
	}
	_ = rebound.Close()
}

// TestOAuthDeadlineReleasesTheRealPort is the socket half of the deadline
// test: after Start times out, the port it bound must be free.
func TestOAuthDeadlineReleasesTheRealPort(t *testing.T) {
	vault, _ := newTestBroker(t, &allowGate{})
	var boundPort string
	broker, err := NewOAuthBroker(testOAuthConfig(), OAuthDeps{
		Vault: vault, Clock: fixedClock{at: time.Unix(1, 0)}, Deadline: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOAuthBroker: %v", err)
	}
	inner := broker.listen
	broker.listen = func(ctx context.Context) (callbackListener, error) {
		l, lerr := inner(ctx)
		if lerr == nil {
			boundPort = l.Port()
		}
		return l, lerr
	}
	broker.open = func(context.Context, string) error { return nil }
	if _, err := broker.Start(context.Background()); err == nil {
		t.Fatal("the deadline did not fire")
	}
	rebound, err := net.Listen("tcp", provider.LoopbackHost+":"+boundPort)
	if err != nil {
		t.Fatalf("port %s is still bound after the deadline: %v", boundPort, err)
	}
	_ = rebound.Close()
}

// TestOAuthNoInputBindsNoPort is the socket half of the CASCADE_NO_INPUT
// assertion: the refusal must not have taken a port at all.
func TestOAuthNoInputBindsNoPort(t *testing.T) {
	t.Setenv(noInputEnv, "1")
	vault, _ := newTestBroker(t, &allowGate{})
	broker, err := NewOAuthBroker(testOAuthConfig(), OAuthDeps{Vault: vault, Clock: fixedClock{at: time.Unix(1, 0)}})
	if err != nil {
		t.Fatalf("NewOAuthBroker: %v", err)
	}
	probe, err := net.Listen("tcp", provider.LoopbackHost+":0")
	if err != nil {
		t.Fatalf("probing for a free port: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()
	if _, err := broker.Start(context.Background()); err == nil {
		t.Fatal("CASCADE_NO_INPUT=1 did not refuse")
	}
	rebound, err := net.Listen("tcp", provider.LoopbackHost+":"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("the refusal left a port bound: %v", err)
	}
	_ = rebound.Close()
}

// TestOAuthBrowserCommandIsShellFree asserts the opener never builds a
// shell command line: the URL is one argv element, so nothing inside it can
// be interpreted as a command.
func TestOAuthBrowserCommandIsShellFree(t *testing.T) {
	const hostile = "https://idp.example/authorize?x=$(rm -rf /);`id`"
	name, args, ok := browserCommand(hostile)
	if !ok {
		t.Skipf("no browser opener on GOOS; nothing to assert")
	}
	for _, shell := range []string{"sh", "bash", "zsh", "cmd", "powershell"} {
		if strings.HasSuffix(name, shell) || strings.HasSuffix(name, shell+".exe") {
			t.Fatalf("the opener runs a shell (%s)", name)
		}
	}
	found := false
	for _, arg := range args {
		if arg == hostile {
			found = true
		}
	}
	if !found {
		t.Fatalf("the URL was not passed as a single argv element: %v", args)
	}
}
