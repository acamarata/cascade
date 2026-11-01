package provider_test

// Purpose (this file): the transient marker keeps the error a caller sees
//   and is read back through wrapping layers; the cause set is closed.
// SPORT: pkg.provider.transient-error-class (TEST) - P1-GWY-03.

import (
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestTransientMarkerRoundTrip(t *testing.T) {
	base := cascade.New(cascade.KindUnavailable, "upstream: http 503")
	info := provider.TransientInfo{Cause: provider.TransientHTTP5xx, Status: 503, RetryAfter: 2 * time.Second}
	marked := provider.MarkTransient(base, info)

	if marked.Error() != base.Error() {
		t.Fatalf("message = %q, want %q unchanged", marked.Error(), base.Error())
	}
	var ce *cascade.Error
	if !errors.As(marked, &ce) || ce != base || ce.Kind != cascade.KindUnavailable || ce.Msg != "upstream: http 503" {
		t.Fatalf("errors.As reached %+v, want the original error by identity", ce)
	}
	if errors.Unwrap(marked) != base {
		t.Fatal("Unwrap does not reach the original error")
	}
	if cascade.NewRPCError(marked).Code != cascade.NewRPCError(base).Code {
		t.Fatal("the wire code changed under the mark")
	}

	wrapped := cascade.Wrap(cascade.KindInternal, cascade.Wrap(cascade.KindUnavailable, marked, "inner"), "outer")
	got, ok := provider.TransientOf(wrapped)
	if !ok || got != info {
		t.Fatalf("TransientOf through two Wrap layers = %+v, %v; want %+v, true", got, ok, info)
	}
	if _, ok := provider.TransientOf(base); ok {
		t.Fatal("an unmarked error reads as transient")
	}
	if _, ok := provider.TransientOf(nil); ok {
		t.Fatal("nil reads as transient")
	}
	if provider.MarkTransient(nil, info) != nil {
		t.Fatal("MarkTransient(nil) is not nil")
	}
}

func TestTransientCauseClosedSet(t *testing.T) {
	members := []provider.TransientCause{
		provider.TransientHTTP5xx, provider.TransientOverloaded,
		provider.TransientStreamAPIError, provider.TransientTransport,
	}
	want := []string{"http_5xx", "overloaded", "stream_api_error", "transport"}
	for i, c := range members {
		if string(c) != want[i] || !c.Valid() {
			t.Fatalf("cause %d = %q (valid %v), want %q valid", i, c, c.Valid(), want[i])
		}
	}
	for _, c := range []provider.TransientCause{"", "http_4xx", "rate_limit", "HTTP_5XX", "timeout"} {
		if c.Valid() {
			t.Fatalf("cause %q is valid, want outside the closed set", c)
		}
	}
}
