//go:build !windows && integration

package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestJobsEvidenceBrowserShapedRefused asserts that a browser-shaped request
// to the evidence SSE endpoint is refused with HTTP 403 by guardLocalRequest.
func TestJobsEvidenceBrowserShapedRefused(t *testing.T) {
	d := evidenceDaemonFixture(t)
	r, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost/events?topic=jobs.evidence&ticket=A", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Origin", "http://evil.example.com")
	res, err := d.http.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("browser-shaped request status: got %d, want %d", res.StatusCode, http.StatusForbidden)
	}
}

// TestJobsEvidenceBrowserShapedBody asserts the refusal body mentions
// the browser-shaped rule from internal/rpc/request_guard.go.
func TestJobsEvidenceBrowserShapedBody(t *testing.T) {
	d := evidenceDaemonFixture(t)
	r, err := http.NewRequestWithContext(context.Background(), "GET", "http://localhost/events?topic=jobs.evidence&ticket=A", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := d.http.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusForbidden || !strings.Contains(string(raw), "browser-shaped request refused") {
		t.Fatalf("unexpected refusal: %d %s", res.StatusCode, raw)
	}
}
