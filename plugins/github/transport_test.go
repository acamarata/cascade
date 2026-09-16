package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/github/auth"
)

// Purpose (this file): the transports' testable halves — the request this
//   plugin builds for the token endpoint, the capped read of its response,
//   and the Loopback methods that need no bound socket.
// Constraints: this package may name net types; plugins/github/auth may
//   not, which is why the DECISIONS live over there and are tested there.
//   What genuinely needs a listener is covered by the integration lane.
// SPORT: plugins/github tests (ADD) — P1-E25-W5-S51-T1.

// TestTokenRequestAsksForJSON pins the header that decides the response
// FORMAT. GitHub's token endpoint answers form-encoded by default; without
// this the body decodes to a zero TokenResponse — no token, no error — and
// the failure surfaces as a confusing integrity error rather than as the
// missing header it actually is.
func TestTokenRequestAsksForJSON(t *testing.T) {
	req, err := buildTokenRequest(context.Background(), auth.TokenEndpoint,
		url.Values{"code": {"c"}, "code_verifier": {"v"}})
	if err != nil {
		t.Fatalf("buildTokenRequest: %v", err)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q; GitHub answers form-encoded without this", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", got)
	}
	if req.Method != "POST" {
		t.Errorf("method = %q", req.Method)
	}
	if req.URL.String() != auth.TokenEndpoint {
		t.Errorf("url = %q, want the token endpoint", req.URL.String())
	}
	if req.Body == nil {
		t.Error("the token request carries no form body")
	}
}

// TestTokenRequestRefusesAnUnusableEndpoint covers the build error path.
func TestTokenRequestRefusesAnUnusableEndpoint(t *testing.T) {
	if _, err := buildTokenRequest(context.Background(), "://not a url", nil); err == nil {
		t.Fatal("an unusable endpoint was accepted")
	}
}

// TestReadTokenBodyCapsWhatItReads proves a far end that never stops
// writing cannot grow this process without bound.
func TestReadTokenBodyCapsWhatItReads(t *testing.T) {
	body, err := readTokenBody(strings.NewReader(strings.Repeat("x", maxTokenBody+4096)))
	if err != nil {
		t.Fatalf("readTokenBody: %v", err)
	}
	if len(body) != maxTokenBody {
		t.Fatalf("read %d bytes, want the read capped at %d", len(body), maxTokenBody)
	}
}

// TestReadTokenBodyReportsAReadFailure proves a truncated response is a
// typed error rather than a short body decoded as a token.
func TestReadTokenBodyReportsAReadFailure(t *testing.T) {
	_, err := readTokenBody(failingReader{})
	if err == nil {
		t.Fatal("a failing read reported success")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

// TestReadTokenBodyReadsAShortResponse keeps the two checks above honest.
func TestReadTokenBodyReadsAShortResponse(t *testing.T) {
	body, err := readTokenBody(strings.NewReader(`{"access_token":"x"}`))
	if err != nil {
		t.Fatalf("readTokenBody: %v", err)
	}
	if string(body) != `{"access_token":"x"}` {
		t.Fatalf("body = %q", body)
	}
}

// failingReader fails every read, standing in for a connection that drops
// mid-response.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// TestWaitDelegatesToAwaitCallback proves the exported entry point really
// routes to the logic the tests above pin, rather than carrying a second
// copy of the same decision.
func TestWaitDelegatesToAwaitCallback(t *testing.T) {
	pkce, err := auth.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	incoming := make(chan url.Values, 1)
	incoming <- url.Values{"code": {"the-code"}, "state": {pkce.State}}

	// A Loopback with only its channel: no socket is bound, which is the
	// point — Wait's decision does not need one.
	lb := &Loopback{incoming: incoming}
	code, err := lb.Wait(context.Background(), pkce)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != "the-code" {
		t.Errorf("code = %q", code)
	}
}

// TestClosingAnUnstartedLoopbackIsSafe proves a caller's deferred Close is
// unconditional — it runs even when Listen never succeeded.
func TestClosingAnUnstartedLoopbackIsSafe(t *testing.T) {
	if err := (&Loopback{}).Close(); err != nil {
		t.Fatalf("closing an unstarted loopback: %v", err)
	}
}
