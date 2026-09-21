// Purpose: github_ci_doer.go -- the composition root's net/http adapter.
// Both of Do's error returns are reached WITHOUT a network (an unusable
// method, and a scheme net/http itself refuses to dial), and no import of
// "net"/"net/http" appears here: internal/build's TestNoNetworkUnitTest
// forbids one in any unit test, which is why githubFlattenHeader takes the
// unnamed map type.
//
// SPORT: cmd/cascade:github-ci-doer (TESTED) -- P1-E25-W5-S51-T3.
package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestNewGitHubTokenDoer_CarriesTheToken proves the constructor the two
// verbs inject builds the authenticated adapter, with the token it was
// handed rather than an anonymous client.
func TestNewGitHubTokenDoer_CarriesTheToken(t *testing.T) {
	doer, ok := newGitHubTokenDoer("tok").(*githubHTTPDoer)
	if !ok {
		t.Fatalf("newGitHubTokenDoer returned %T, want the production adapter", newGitHubTokenDoer("tok"))
	}
	if doer.token != "tok" {
		t.Fatalf("token = %q, want the one supplied", doer.token)
	}
	if doer.client == nil || doer.client.Timeout != githubHTTPTimeout {
		t.Fatalf("client = %#v, want one bounded by githubHTTPTimeout", doer.client)
	}
}

// TestGitHubHTTPDoer_FailurePathsAreTyped covers both error returns of Do.
func TestGitHubHTTPDoer_FailurePathsAreTyped(t *testing.T) {
	doer := newGitHubTokenDoer("tok")
	_, err := doer.Do(context.Background(), ci.HTTPRequest{Method: "bad method", URL: "https://api.github.com/x"})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("bad method: err = %v, want KindInvalidInput", err)
	}
	_, err = doer.Do(context.Background(), ci.HTTPRequest{
		Method: "GET", URL: "unsupported://api.github.com/x", Headers: map[string]string{"Accept": "application/json"},
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("unsupported scheme: err = %v, want KindUnavailable", err)
	}
}

// TestGitHubFlattenHeader pins the first-value rule and the two empty
// cases the ETag cache depends on.
func TestGitHubFlattenHeader(t *testing.T) {
	if got := githubFlattenHeader(nil); got != nil {
		t.Fatalf("githubFlattenHeader(nil) = %v, want nil", got)
	}
	got := githubFlattenHeader(map[string][]string{"Etag": {`W/"abc"`, `W/"def"`}, "Empty": {}})
	if got["Etag"] != `W/"abc"` {
		t.Fatalf("Etag = %q, want the first value", got["Etag"])
	}
	if _, present := got["Empty"]; present {
		t.Fatal("a valueless header key must not appear")
	}
}
