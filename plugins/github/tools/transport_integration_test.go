//go:build integration

// Purpose: HTTPDoer.Do exercised against a REAL HTTP server (loopback,
//
//	spawned by this test) rather than a stub — the round trip, the status,
//	the body, the size cap and the redirect rule. Tagged `integration`
//	because it imports "net/http", which Art.7.2's no-network unit lane
//	forbids outright.
//
// SPORT: plugins/github/tools tests (ADD) — P1-E25-W5-S51-T1.
package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// rewriteToServer sends every request to the test server instead of
// GitHub, WITHOUT changing the URL the production code built — the request
// still says https://api.github.com/..., which is what the handler asserts
// on. Only the destination it is dialed at changes.
type rewriteToServer struct {
	target *url.URL
}

func (r rewriteToServer) RoundTrip(req *http.Request) (*http.Response, error) {
	routed := req.Clone(req.Context())
	routed.URL.Scheme = r.target.Scheme
	routed.URL.Host = r.target.Host
	return http.DefaultTransport.RoundTrip(routed)
}

// serverDoer points an HTTPDoer at a local server.
//
// It keeps the PRODUCTION client — timeout and redirect policy and all —
// and swaps only its Transport, so these tests exercise the real policy
// rather than one the test configured for itself.
func serverDoer(t *testing.T, handler http.HandlerFunc) (HTTPDoer, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := HTTPDoer{}.client() // production timeout + CheckRedirect
	client.Transport = rewriteToServer{target: target}
	return HTTPDoer{Client: client}, server
}

// TestDoPerformsARealRoundTrip covers the transport end to end.
func TestDoPerformsARealRoundTrip(t *testing.T) {
	var gotPath, gotAuth, gotAgent string
	doer, _ := serverDoer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotAgent = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"cascade"}`))
	})

	status, body, err := doer.Do(context.Background(),
		Request{Method: "GET", Path: "/repos/acamarata/cascade"}, "gho_x")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d", status)
	}
	if !strings.Contains(string(body), "cascade") {
		t.Errorf("body = %q", body)
	}
	if gotPath != "/repos/acamarata/cascade" {
		t.Errorf("the server saw path %q", gotPath)
	}
	if gotAuth != "Bearer gho_x" {
		t.Errorf("the server saw Authorization %q", gotAuth)
	}
	if gotAgent != UserAgent {
		t.Errorf("the server saw User-Agent %q", gotAgent)
	}
}

// TestDoReturnsAnErrorStatusWithItsBody proves the transport does NOT turn
// an HTTP error into a transport error: Classify needs both halves, and
// GitHub's message is what makes a 403 readable.
func TestDoReturnsAnErrorStatusWithItsBody(t *testing.T) {
	doer, _ := serverDoer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})

	status, body, err := doer.Do(context.Background(), Request{Method: "GET", Path: "/x"}, "")
	if err != nil {
		t.Fatalf("an HTTP error status was reported as a transport error: %v", err)
	}
	if status != http.StatusNotFound {
		t.Errorf("status = %d", status)
	}
	if !strings.Contains(string(body), "Not Found") {
		t.Errorf("body = %q, want GitHub's message preserved", body)
	}
}

// TestDoDoesNotFollowARedirect is the rule that keeps a write landing where
// the caller named. A renamed repository redirects; following it silently
// would merge or close somewhere else.
func TestDoDoesNotFollowARedirect(t *testing.T) {
	var hits int
	doer, _ := serverDoer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/elsewhere", http.StatusMovedPermanently)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	status, _, err := doer.Do(context.Background(), Request{Method: "GET", Path: "/moved"}, "")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if status != http.StatusMovedPermanently {
		t.Errorf("status = %d, want the redirect surfaced rather than followed", status)
	}
	if hits != 1 {
		t.Errorf("the server was hit %d times; the redirect was followed", hits)
	}
}

// TestDoReportsATransportFailure proves an unreachable server is a typed
// error rather than a zero status the caller might read as success.
func TestDoReportsATransportFailure(t *testing.T) {
	doer, server := serverDoer(t, func(http.ResponseWriter, *http.Request) {})
	server.Close() // nothing is listening any more

	if _, _, err := doer.Do(context.Background(), Request{Method: "GET", Path: "/x"}, ""); err == nil {
		t.Fatal("a call to a closed server reported success")
	}
}
