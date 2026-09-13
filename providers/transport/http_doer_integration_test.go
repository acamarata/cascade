//go:build integration

// Purpose: the real-socket proof for netTransport.Send/newRequest and
// OpenAIDoer.Do, the functions the default unit lane cannot exercise
// because they require "net/http" (see http_doer.go's header comment and
// internal/build/hygiene.go's NoNetworkUnitTestScanFile). Run with
// `go test -tags=integration ./providers/transport/...`; this file
// contributes nothing to the default-lane coverage measurement the Art.4
// floor gate reads (12-QUALITY-CONSTITUTION.md), by design.
package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNetTransport_RealRoundTrip(t *testing.T) {
	var gotMethod, gotHeader string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotHeader = r.Header.Get("x-test")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	tr := netTransport{client: srv.Client()}
	status, headers, body, err := tr.Send(context.Background(), "POST", srv.URL, map[string]string{"x-test": "v"}, []byte("payload"))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	defer body.Close()

	if status != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", status, http.StatusTeapot)
	}
	if len(headers["Retry-After"]) == 0 || headers["Retry-After"][0] != "7" {
		t.Fatalf("headers[Retry-After] = %v, want [7]", headers["Retry-After"])
	}
	got, _ := io.ReadAll(body)
	if string(got) != "hello" {
		t.Fatalf("body = %q, want hello", got)
	}
	if gotMethod != "POST" || gotHeader != "v" || string(gotBody) != "payload" {
		t.Fatalf("server saw method=%q header=%q body=%q", gotMethod, gotHeader, gotBody)
	}
}

func TestNetTransport_RealRoundTripError(t *testing.T) {
	tr := netTransport{client: http.DefaultClient}
	if _, _, _, err := tr.Send(context.Background(), "\x7f", "http://127.0.0.1:0", nil, nil); err == nil {
		t.Fatal("Send: want error for an invalid method, got nil")
	}
}

// TestOpenAIDoer_RealRoundTrip proves OpenAIDoer's *http.Request/
// *http.Response translation (the reverse direction from the other three
// adapters): building a real *http.Request needs net/http, so this is
// this doer's only exercise outside the default unit lane's fakeTransport
// tests, which cover its pure flattenHeader half.
func TestOpenAIDoer_RealRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("server saw Authorization=%q, want Bearer k", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Errorf("server saw body=%q, want payload", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	d := OpenAIDoer{Transport: netTransport{client: srv.Client()}}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	req.Body = io.NopCloser(strings.NewReader("payload"))
	req.Header.Set("Authorization", "Bearer k")

	resp, err := d.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "ok" {
		t.Fatalf("body = %q, want ok", got)
	}
}
