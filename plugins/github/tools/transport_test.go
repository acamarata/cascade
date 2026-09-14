package tools

import (
	"context"
	"strings"
	"testing"
)

// Purpose (this file): the request HTTPDoer would send, asserted without a
//   socket and without naming an http type.
// Constraints: Art.7.2 forbids an untagged _test.go from IMPORTING
//   net/http. It does not forbid calling a function that returns one: Go
//   needs the import only to NAME a type, so build's result can be
//   inspected through its own methods here. What genuinely needs a round
//   trip — Do, HTTPPoster, and the loopback listener — is covered by the
//   integration-tagged tests instead.
// SPORT: plugins/github/tools tests (ADD) — P1-E25-W5-S51-T1.

// TestBuildTargetsTheSingleDeclaredHost is the egress assertion. This
// plugin declares exactly one net scope, api.github.com, and the URL is
// assembled here from a path — never taken from a caller or a response — so
// there is no value an attacker could supply that moves the call elsewhere.
func TestBuildTargetsTheSingleDeclaredHost(t *testing.T) {
	req, err := HTTPDoer{}.build(context.Background(),
		Request{Method: "GET", Path: "/repos/acamarata/cascade"}, "gho_x")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := req.URL.String(); got != APIBase+"/repos/acamarata/cascade" {
		t.Fatalf("url = %q, want it rooted at %q", got, APIBase)
	}
	if req.URL.Host != "api.github.com" {
		t.Errorf("host = %q, want the single declared scope", req.URL.Host)
	}
	if req.Method != "GET" {
		t.Errorf("method = %q", req.Method)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer gho_x" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("User-Agent"); got != UserAgent {
		t.Errorf("User-Agent = %q; GitHub rejects a request without one", got)
	}
}

// TestBuildCarriesTheBodyAndItsType proves a write request is sent as JSON.
func TestBuildCarriesTheBodyAndItsType(t *testing.T) {
	req, err := HTTPDoer{}.build(context.Background(),
		Request{Method: "POST", Path: "/repos/a/b/issues", Body: []byte(`{"title":"t"}`)}, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if req.Body == nil {
		t.Fatal("the request carries no body")
	}
	if _, present := req.Header["Authorization"]; present {
		t.Error("an empty token produced an Authorization header")
	}
}

// TestBuildRefusesAnUnusableMethod proves a malformed request fails as a
// typed error here rather than somewhere further down the transport.
func TestBuildRefusesAnUnusableMethod(t *testing.T) {
	_, err := HTTPDoer{}.build(context.Background(),
		Request{Method: "BAD METHOD", Path: "/x"}, "")
	if err == nil {
		t.Fatal("a request with an unusable method was built")
	}
	if !strings.Contains(err.Error(), "github:") {
		t.Errorf("error = %v, want it attributed to this plugin", err)
	}
}

// TestDefaultClientDoesNotFollowRedirects pins the redirect rule. GitHub
// answers a renamed repository with a redirect to a different path;
// following it silently would make a merge or a close land somewhere the
// caller never named.
func TestDefaultClientDoesNotFollowRedirects(t *testing.T) {
	client := HTTPDoer{}.client()
	if client == nil {
		t.Fatal("the zero HTTPDoer produced no client")
	}
	if client.CheckRedirect == nil {
		t.Fatal("the default client follows redirects")
	}
	if err := client.CheckRedirect(nil, nil); err == nil {
		t.Error("CheckRedirect permitted a redirect")
	}
	if client.Timeout != RequestTimeout {
		t.Errorf("timeout = %v, want %v", client.Timeout, RequestTimeout)
	}
}

// TestAConfiguredClientIsUsedAsGiven proves the injected client wins over
// the default — the seam the integration tests use to point this transport
// at a local server instead of GitHub.
func TestAConfiguredClientIsUsedAsGiven(t *testing.T) {
	custom := HTTPDoer{}.client()
	custom.Timeout = 1

	got := HTTPDoer{Client: custom}.client()
	if got != custom {
		t.Fatal("a configured client was replaced by the default one")
	}
	if got.Timeout != 1 {
		t.Errorf("timeout = %v, want the configured client's own", got.Timeout)
	}
}
