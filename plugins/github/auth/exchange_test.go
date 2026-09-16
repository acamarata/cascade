package auth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose (this file): the token exchange, driven through the Poster seam
//   so the default unit lane never opens a socket (Art.7.2).
// SPORT: plugins/github/auth tests (ADD) — P1-E25-W5-S51-T1.

// recordedPost captures the form an exchange would send.
type recordedPost struct {
	status int
	body   string
	err    error

	endpoint string
	form     url.Values
	calls    int
}

func (r *recordedPost) post(_ context.Context, endpoint string, form url.Values) (int, []byte, error) {
	r.calls++
	r.endpoint = endpoint
	r.form = form
	if r.err != nil {
		return 0, nil, r.err
	}
	status := r.status
	if status == 0 {
		status = 200
	}
	return status, []byte(r.body), nil
}

func testConfig(t *testing.T) provider.ProviderOAuthConfig {
	t.Helper()
	cfg, err := Config("Iv1.test")
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	return cfg
}

// TestExchangeSendsTheProofAndNoSecret pins what goes to GitHub. The
// verifier is what proves this process started the flow; a client secret is
// what a public client must never have, because shipping one puts it in
// every user's filesystem.
func TestExchangeSendsTheProofAndNoSecret(t *testing.T) {
	post := &recordedPost{body: `{"access_token":"gho_x","token_type":"bearer","scope":"repo"}`}

	got, err := Exchange(context.Background(), post.post, testConfig(t),
		"the-code", "the-verifier", "http://127.0.0.1:9/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if got.AccessToken != "gho_x" {
		t.Errorf("access token = %q", got.AccessToken)
	}
	if post.endpoint != TokenEndpoint {
		t.Errorf("endpoint = %q, want %q", post.endpoint, TokenEndpoint)
	}
	for field, want := range map[string]string{
		"grant_type":    "authorization_code",
		"code":          "the-code",
		"code_verifier": "the-verifier",
		"redirect_uri":  "http://127.0.0.1:9/callback",
	} {
		if post.form.Get(field) != want {
			t.Errorf("form[%q] = %q, want %q", field, post.form.Get(field), want)
		}
	}
	if post.form.Get("client_secret") != "" {
		t.Error("the exchange carried a client_secret; this is a public client")
	}
}

// TestExchangeCatchesA200Failure is the shape assertion that matters most.
// GitHub answers a failed exchange with HTTP 200 and an `error` member, so
// a caller checking only the status would store a non-token as a token.
func TestExchangeCatchesA200Failure(t *testing.T) {
	post := &recordedPost{status: 200, body: `{"error":"bad_verification_code","error_description":"expired"}`}

	_, err := Exchange(context.Background(), post.post, testConfig(t),
		"c", "v", "http://127.0.0.1:9/callback")
	if err == nil {
		t.Fatal("a 200 carrying an error member was accepted as a token")
	}
	if !strings.Contains(err.Error(), "bad_verification_code") {
		t.Errorf("error = %v, want GitHub's reason", err)
	}
}

// TestExchangeRefusesAnErrorStatus covers the ordinary failure path.
func TestExchangeRefusesAnErrorStatus(t *testing.T) {
	post := &recordedPost{status: 401, body: `{}`}
	_, err := Exchange(context.Background(), post.post, testConfig(t),
		"c", "v", "http://127.0.0.1:9/callback")
	if err == nil {
		t.Fatal("a 401 from the token endpoint was accepted")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
		t.Errorf("kind = %v (ok=%v), want KindPermissionDenied", kind, ok)
	}
}

// TestExchangeRefusesAnUndecodableBody proves a non-JSON answer (a proxy's
// HTML error page, say) fails as an integrity error rather than yielding a
// zero token.
func TestExchangeRefusesAnUndecodableBody(t *testing.T) {
	post := &recordedPost{body: `<html>502 Bad Gateway</html>`}
	_, err := Exchange(context.Background(), post.post, testConfig(t),
		"c", "v", "http://127.0.0.1:9/callback")
	if err == nil {
		t.Fatal("an HTML body decoded as a token response")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

// TestExchangeReportsATransportFailure separates "the call failed" from
// "GitHub refused".
func TestExchangeReportsATransportFailure(t *testing.T) {
	post := &recordedPost{err: errors.New("dial tcp: refused")}
	_, err := Exchange(context.Background(), post.post, testConfig(t),
		"c", "v", "http://127.0.0.1:9/callback")
	if err == nil {
		t.Fatal("a transport failure was reported as success")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestExchangeValidatesBeforeSending proves a missing code or verifier
// costs no round trip — and, more to the point, that an exchange is never
// attempted without the proof that makes it legitimate.
func TestExchangeValidatesBeforeSending(t *testing.T) {
	cfg := testConfig(t)
	for _, tc := range []struct{ name, code, verifier string }{
		{"no code", "  ", "v"},
		{"no verifier", "c", "  "},
	} {
		post := &recordedPost{body: `{"access_token":"x","token_type":"bearer"}`}
		if _, err := Exchange(context.Background(), post.post, cfg, tc.code, tc.verifier, "u"); err == nil {
			t.Errorf("%s: the exchange was attempted anyway", tc.name)
		}
		if post.calls != 0 {
			t.Errorf("%s: %d request(s) went out", tc.name, post.calls)
		}
	}
	if _, err := Exchange(context.Background(), nil, cfg, "c", "v", "u"); err == nil {
		t.Error("an exchange with no transport reported success")
	}
}
