package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// envOf builds a getenv over a fixed map, so the no-input rule is exercised
// without mutating the real process environment.
func envOf(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// TestConfigDescribesGitHub pins the authorization server and the client
// shape. The redirect URI carrying NO port is the load-bearing detail: the
// broker binds an ephemeral one, and a hard-coded port either collides with
// whatever holds it or lets another process squat the callback.
func TestConfigDescribesGitHub(t *testing.T) {
	cfg, err := Config("client-123")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.AuthEndpoint != AuthEndpoint || cfg.TokenEndpoint != TokenEndpoint {
		t.Fatalf("endpoints = %q / %q", cfg.AuthEndpoint, cfg.TokenEndpoint)
	}
	if cfg.PKCEMethod != provider.PKCEMethodS256 {
		t.Errorf("PKCEMethod = %v, want S256", cfg.PKCEMethod)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != RequiredScope {
		t.Errorf("Scopes = %v, want exactly [%s]", cfg.Scopes, RequiredScope)
	}
	if !strings.Contains(cfg.RedirectURI, provider.LoopbackHost) {
		t.Errorf("RedirectURI = %q, want the loopback literal", cfg.RedirectURI)
	}
	if strings.Contains(strings.TrimPrefix(cfg.RedirectURI, "http://"), ":") {
		t.Errorf("RedirectURI = %q, want no port: the broker binds an ephemeral one", cfg.RedirectURI)
	}
	if strings.Contains(cfg.RedirectURI, "localhost") {
		t.Error("RedirectURI names localhost; a poisoned resolver could point it off this machine")
	}
	if _, err := Config("  "); err == nil {
		t.Error("Config accepted a blank client id")
	}
}

// TestNewPKCEDerivesAVerifiableChallenge proves the challenge really is the
// S256 hash of the verifier. A challenge computed any other way would be
// rejected by the authorization server at exchange time, which is the worst
// place to find out.
func TestNewPKCEDerivesAVerifiableChallenge(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatalf("NewPKCE: %v", err)
	}
	sum := sha256.Sum256([]byte(p.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if p.Challenge != want {
		t.Fatalf("challenge does not match S256(verifier)")
	}
	if strings.ContainsAny(p.Challenge+p.Verifier+p.State, "+/=") {
		t.Error("PKCE values are not base64url without padding")
	}
	if len(p.Verifier) < 43 {
		t.Errorf("verifier is %d chars, below RFC 7636's floor", len(p.Verifier))
	}
}

// TestNewPKCEIsFreshEveryTime proves the values come from randomness rather
// than a constant: a reused state would let one flow's callback satisfy
// another's.
func TestNewPKCEIsFreshEveryTime(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 16; i++ {
		p, err := NewPKCE()
		if err != nil {
			t.Fatal(err)
		}
		if seen[p.State] || seen[p.Verifier] {
			t.Fatal("NewPKCE repeated a value across calls")
		}
		seen[p.State], seen[p.Verifier] = true, true
	}
}

// TestAuthorizeURLCarriesEveryParameter pins what the browser is sent.
func TestAuthorizeURLCarriesEveryParameter(t *testing.T) {
	cfg, err := Config("client-123")
	if err != nil {
		t.Fatal(err)
	}
	p := PKCE{Verifier: "v", Challenge: "chal", State: "st"}
	raw, err := AuthorizeURL(cfg, p, "http://127.0.0.1:54321/callback")
	if err != nil {
		t.Fatalf("AuthorizeURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("AuthorizeURL produced an unparseable URL: %v", err)
	}
	q := parsed.Query()
	for key, want := range map[string]string{
		"client_id":             "client-123",
		"redirect_uri":          "http://127.0.0.1:54321/callback",
		"scope":                 RequiredScope,
		"state":                 "st",
		"code_challenge":        "chal",
		"code_challenge_method": "S256",
	} {
		if q.Get(key) != want {
			t.Errorf("query[%q] = %q, want %q", key, q.Get(key), want)
		}
	}
	if strings.Contains(raw, p.Verifier) {
		t.Fatal("the authorization URL carries the PKCE verifier; only the challenge may leave this process")
	}
}

// TestAuthorizeURLRefusesAnIncompleteFlow covers the guards.
func TestAuthorizeURLRefusesAnIncompleteFlow(t *testing.T) {
	cfg, _ := Config("c")
	if _, err := AuthorizeURL(cfg, PKCE{Challenge: "c", State: "s"}, ""); err == nil {
		t.Error("a redirect URI with no bound port was accepted")
	}
	if _, err := AuthorizeURL(cfg, PKCE{State: "s"}, "http://127.0.0.1:1/callback"); err == nil {
		t.Error("a flow with no challenge was accepted")
	}
	if _, err := AuthorizeURL(cfg, PKCE{Challenge: "c"}, "http://127.0.0.1:1/callback"); err == nil {
		t.Error("a flow with no state was accepted")
	}
}

// TestVerifyCallbackRequiresMatchingState is the assertion that makes the
// loopback listener safe. Without it the broker would accept an
// authorization code from anything that can reach the port — which is every
// process on the machine.
func TestVerifyCallbackRequiresMatchingState(t *testing.T) {
	p := PKCE{State: "the-real-state"}

	code, err := VerifyCallback(url.Values{"state": {"the-real-state"}, "code": {"abc"}}, p)
	if err != nil || code != "abc" {
		t.Fatalf("a matching callback returned %q, %v", code, err)
	}

	for name, query := range map[string]url.Values{
		"wrong state":  {"state": {"someone-elses"}, "code": {"abc"}},
		"absent state": {"code": {"abc"}},
		"absent code":  {"state": {"the-real-state"}},
		"empty state":  {"state": {""}, "code": {"abc"}},
	} {
		if _, err := VerifyCallback(query, p); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestVerifyCallbackReportsAServerRefusalFirst proves a declined
// authorization is reported with its own reason rather than as a generic
// missing-code error, which would send the operator looking in the wrong
// place.
func TestVerifyCallbackReportsAServerRefusalFirst(t *testing.T) {
	query := url.Values{
		"error":             {"access_denied"},
		"error_description": {"The user denied the request"},
		"state":             {"mismatched-on-purpose"},
	}
	_, err := VerifyCallback(query, PKCE{State: "the-real-state"})
	if err == nil {
		t.Fatal("a declined authorization was accepted")
	}
	if !strings.Contains(err.Error(), "denied the request") {
		t.Errorf("error = %v, want the server's own description", err)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Errorf("kind = %v (typed=%v), want %v", kind, ok, cascade.KindPermissionDenied)
	}
}

// TestRequireInteractiveRefusesUnderNoInput pins the §5.8 rule: OAuth needs
// a person at a browser, so a process that cannot prompt must fail loudly
// and say what to do instead.
func TestRequireInteractiveRefusesUnderNoInput(t *testing.T) {
	err := RequireInteractive(envOf(map[string]string{NoInputEnv: "1"}))
	if err == nil {
		t.Fatal("authorization proceeded with CASCADE_NO_INPUT=1")
	}
	if !strings.Contains(err.Error(), NoInputEnv) {
		t.Errorf("error = %v, want it to name the variable", err)
	}
	if !strings.Contains(err.Error(), "interactive terminal") {
		t.Errorf("error = %v, want it to say what to do instead", err)
	}

	for _, value := range []string{"", "0", "true", "yes"} {
		if err := RequireInteractive(envOf(map[string]string{NoInputEnv: value})); err != nil {
			t.Errorf("CASCADE_NO_INPUT=%q refused the flow: %v", value, err)
		}
	}
	if err := RequireInteractive(nil); err == nil {
		t.Error("RequireInteractive accepted a nil environment reader")
	}
}

// TestTokenResponseValidateCatchesA200Failure is the case a status-code
// check alone would miss: GitHub answers a failed exchange with HTTP 200
// and an `error` member, so a caller trusting the status would store an
// empty token and discover it one 401 at a time.
func TestTokenResponseValidateCatchesA200Failure(t *testing.T) {
	err := TokenResponse{Error: "bad_verification_code", ErrorDesc: "The code passed is incorrect"}.Validate()
	if err == nil {
		t.Fatal("a token response carrying an error member validated")
	}
	if !strings.Contains(err.Error(), "code passed is incorrect") {
		t.Errorf("error = %v, want the server's description", err)
	}

	if err := (TokenResponse{}).Validate(); err == nil {
		t.Fatal("a response with neither a token nor an error validated")
	}
	if err := (TokenResponse{AccessToken: "gho_x", TokenType: "bearer"}).Validate(); err != nil {
		t.Fatalf("a good token response was refused: %v", err)
	}
}

// TestRandomnessFailureIsATypedError proves a randomness source that
// cannot answer produces an error rather than a short or predictable
// value — the one outcome a PKCE flow must never have.
func TestRandomnessFailureIsATypedError(t *testing.T) {
	_, err := randomURLSafeFrom(exhaustedReader{}, 64)
	if err == nil {
		t.Fatal("a failing randomness source produced a value")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInternal {
		t.Errorf("kind = %v (ok=%v), want KindInternal", kind, ok)
	}
}

// exhaustedReader yields nothing, standing in for a randomness source that
// cannot supply the bytes asked of it.
type exhaustedReader struct{}

func (exhaustedReader) Read([]byte) (int, error) { return 0, io.EOF }

// TestShortRandomnessIsRefused proves a source that returns FEWER bytes
// than asked is refused rather than silently producing a shorter, weaker
// verifier — io.ReadFull is what makes that true.
func TestShortRandomnessIsRefused(t *testing.T) {
	if _, err := randomURLSafeFrom(strings.NewReader("only-a-few"), 64); err == nil {
		t.Fatal("a source supplying fewer bytes than requested was accepted")
	}
}
