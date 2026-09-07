// Purpose: unit tests for auth.go's transport/retry/error-mapping layer:
//
//	New's validation, retryDelay's pure backoff sequence, waitForRetry's
//	frozen-clock deadline refusal (no real sleep on that path), and
//	mapHTTPStatus's coverage of every status this driver maps.
//
// Constraints: no "net"/"net/http" import (Art.7.2); fakeDoer/fakeResponse
//
//	come from openai.go.
//
// SPORT: providers/openai driver/ADD (P1-E10-W3-S19-T3).
package openai

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestNewValidation(t *testing.T) {
	base := Config{
		BaseURL: "https://api.openai.com/v1", KeyRef: "OPENAI_API_KEY",
		Resolver: stubResolver{key: "v"}, HTTPClient: newFakeDoer(), Clock: fixedClock{},
	}
	cases := []struct {
		name    string
		mutate  func(c Config) Config
		wantErr bool
	}{
		{"valid https", func(c Config) Config { return c }, false},
		{"valid loopback http", func(c Config) Config { c.BaseURL = "http://127.0.0.1:8080/v1"; return c }, false},
		{"empty base url", func(c Config) Config { c.BaseURL = ""; return c }, true},
		{"non-loopback http rejected", func(c Config) Config { c.BaseURL = "http://example.com/v1"; return c }, true},
		{"empty key ref", func(c Config) Config { c.KeyRef = ""; return c }, true},
		{"nil resolver", func(c Config) Config { c.Resolver = nil; return c }, true},
		{"nil http client", func(c Config) Config { c.HTTPClient = nil; return c }, true},
		{"nil clock", func(c Config) Config { c.Clock = nil; return c }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.mutate(base))
			if (err != nil) != tc.wantErr {
				t.Fatalf("New: err=%v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil && !cascade.HasKind(err, cascade.KindInvalidInput) {
				t.Fatalf("want KindInvalidInput, got %v", err)
			}
		})
	}
}

func TestRetryDelayIsDeterministic(t *testing.T) {
	base := 10 * time.Millisecond
	if got := retryDelay(0, 0, base); got != base {
		t.Fatalf("attempt 0: got %v, want %v", got, base)
	}
	if got := retryDelay(1, 0, base); got != 2*base {
		t.Fatalf("attempt 1: got %v, want %v", got, 2*base)
	}
	if got := retryDelay(2, 0, base); got != 4*base {
		t.Fatalf("attempt 2: got %v, want %v", got, 4*base)
	}
	if got := retryDelay(3, 7*time.Second, base); got != 7*time.Second {
		t.Fatalf("vendor Retry-After must win: got %v", got)
	}
}

// TestWaitForRetryRefusesPastDeadlineWithNoRealSleep proves the retry
// refusal is decided purely from the injected Clock, advanced in test code
// - never a real time.Sleep. If this ever called the real wait path, the
// deadline (1ms out) would make the test itself either hang or flake; it
// does neither, because the deadline check runs before any timer starts.
func TestWaitForRetryRefusesPastDeadlineWithNoRealSleep(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := fixedClock{now: start}
	d := &Driver{cfg: Config{Clock: clock}}
	ctx, cancel := context.WithDeadline(context.Background(), start.Add(time.Millisecond))
	defer cancel()
	err := d.waitForRetry(ctx, time.Hour)
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Fatalf("want KindTimeout from the clock-driven refusal, got %v", err)
	}
}

// TestWaitForRetryProceedsWithinDeadline exercises the opposite branch: a
// wait comfortably inside the deadline actually waits (a genuine, sub-
// millisecond real wait - not the sleep this ticket forbids simulating a
// frozen instant for).
func TestWaitForRetryProceedsWithinDeadline(t *testing.T) {
	start := time.Now()
	clock := fixedClock{now: start}
	d := &Driver{cfg: Config{Clock: clock}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.waitForRetry(ctx, time.Millisecond); err != nil {
		t.Fatalf("waitForRetry: %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter(map[string][]string{"Retry-After": {"3"}}); got != 3*time.Second {
		t.Fatalf("got %v, want 3s", got)
	}
	if got := parseRetryAfter(map[string][]string{}); got != 0 {
		t.Fatalf("missing header: got %v, want 0", got)
	}
	if got := parseRetryAfter(map[string][]string{"Retry-After": {"not-a-number"}}); got != 0 {
		t.Fatalf("garbage header: got %v, want 0", got)
	}
}

func TestMapHTTPStatusCoversTaxonomy(t *testing.T) {
	cases := map[int]cascade.Kind{
		400: cascade.KindInvalidInput, 422: cascade.KindInvalidInput,
		401: cascade.KindPermissionDenied, 403: cascade.KindPermissionDenied,
		404: cascade.KindNotFound, 408: cascade.KindTimeout, 409: cascade.KindConflict,
		429: cascade.KindQuotaExhausted, 501: cascade.KindUnsupported,
		500: cascade.KindUnavailable, 503: cascade.KindUnavailable,
		418: cascade.KindInvalidInput,
	}
	for status, want := range cases {
		err := mapHTTPStatus(status, []byte(`{"error":{"message":"x"}}`))
		if err.Kind != want {
			t.Fatalf("status %d: got %v, want %v", status, err.Kind, want)
		}
	}
}

func TestExtractErrorMessageFallsBackOnNonJSON(t *testing.T) {
	if got := extractErrorMessage([]byte("plain text body")); got != "plain text body" {
		t.Fatalf("got %q", got)
	}
	if got := extractErrorMessage([]byte("")); got != "(empty response body)" {
		t.Fatalf("got %q", got)
	}
}
