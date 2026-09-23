// Purpose: guardLocalRequest's own negative/positive matrix (R-14.312):
//   the owner-UID check, every browser-shaped-request marker, and the
//   ordering guarantee (refusal precedes Parse/subscribe) on both routes.
// Constraints: only "net/http/httptest" is imported, never "net"/"net/http"
//   itself — the no-network-unit-lane gate (internal/build/hygiene.go)
//   bans a bare "net"/"net/http" import in a non-integration _test.go
//   file, so this file uses the literal method strings "GET"/"POST"
//   throughout, matching handler_test.go's and sse_test.go's own
//   established pattern.
// SPORT: internal.rpc request_guard (guardLocalRequest, socketHosts) [ADD]
// (P1-E04-W6-S146-T1).

package rpc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rpcReq issues a POST RPCPath request with the given ctx/host/
// content-type and any extra headers set, returning the recorder.
func rpcReq(ctx context.Context, h *Handler, host, contentType string, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", RPCPath, strings.NewReader(`{"jsonrpc":"2.0","method":"m","id":1}`)).WithContext(ctx)
	req.Host = host
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// eventsReq issues a GET EventsPath request the same way rpcReq does for
// POST RPCPath (no body/Content-Type — GET carries none).
func eventsReq(ctx context.Context, h *Handler, host string, extra map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", EventsPath, nil).WithContext(ctx)
	req.Host = host
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// ownerRefusalBody and browserRefusalBody are guardLocalRequest's two
// fixed refusal strings (request_guard.go), spelled out once so every
// test below asserts the EXACT body its own refusal reason produces,
// never "one of the two" — a test that accepted either string would stay
// green if the owner-UID check refused an otherwise-valid browser-shape
// probe for the wrong reason (or vice versa), which is exactly the gap
// this ticket's CR found.
const (
	ownerRefusalBody   = "forbidden: socket peer is not the daemon owner"
	browserRefusalBody = "forbidden: browser-shaped request refused"
)

// assertForbiddenBody fails t unless rec is a 403 whose body is exactly
// wantBody, never an echo of anything from the request itself.
func assertForbiddenBody(t *testing.T, rec *httptest.ResponseRecorder, wantBody string) {
	t.Helper()
	if rec.Code != 403 {
		t.Fatalf("status = %d, want 403; body = %q", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != wantBody {
		t.Fatalf("body = %q, want %q", got, wantBody)
	}
}

// newTestEventsHandler builds a Handler with SSEHandler mounted over a
// REAL Bus (t.Cleanup closes it) — every GET /events refusal test below
// uses this so a broken guard would reach the same real Subscribe path
// production does, rather than a nil bus masking the failure behind a nil
// pointer panic instead of a clean 403 assertion.
func newTestEventsHandler(t *testing.T) *Handler {
	t.Helper()
	bus, clock := newTestBus()
	t.Cleanup(func() { _ = bus.Close() })
	sse := NewSSEHandler(bus, "ns", knownAB, clock)
	return NewHandlerWithSSE(NewRegistry(), sse)
}

func TestEventsRefusesNonOwnerPeer(t *testing.T) {
	withOwnerUID(t, 501)
	h := newTestEventsHandler(t)

	rec := eventsReq(ctxWithPeerCred(999, true), h, "unix", nil)
	assertForbiddenBody(t, rec, ownerRefusalBody)
}

func TestEventsRefusesUnresolvedPeer(t *testing.T) {
	withOwnerUID(t, 501)
	h := newTestEventsHandler(t)

	rec := eventsReq(ctxWithPeerCred(501, false), h, "unix", nil)
	assertForbiddenBody(t, rec, ownerRefusalBody)
}

func TestRPCRefusesOriginHeader(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	rec := rpcReq(ctxWithPeerCred(501, true), h, "unix", "application/json", map[string]string{"Origin": "null"})
	assertForbiddenBody(t, rec, browserRefusalBody)
}

func TestEventsRefusesOriginHeader(t *testing.T) {
	withOwnerUID(t, 501)
	h := newTestEventsHandler(t)

	rec := eventsReq(ctxWithPeerCred(501, true), h, "unix", map[string]string{"Origin": "http://evil.example"})
	assertForbiddenBody(t, rec, browserRefusalBody)
}

// TestRPCRefusesEmptyOriginHeader proves the contract's "present (any
// value)" rule: an Origin header set to the empty string still refuses,
// which a bare r.Header.Get("Origin") != "" check (this ticket's CR
// against the earlier request_guard.go, replaced by headerPresent) would
// have missed — no browser sends an empty Origin, but the guard must not
// rely on that.
func TestRPCRefusesEmptyOriginHeader(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	rec := rpcReq(ctxWithPeerCred(501, true), h, "unix", "application/json", map[string]string{"Origin": ""})
	assertForbiddenBody(t, rec, browserRefusalBody)
}

func TestRPCRefusesSecFetchHeaders(t *testing.T) {
	withOwnerUID(t, 501)
	cases := map[string]map[string]string{
		"Sec-Fetch-Site":       {"Sec-Fetch-Site": "cross-site"},
		"Sec-Fetch-Mode":       {"Sec-Fetch-Mode": "no-cors"},
		"Sec-Fetch-Site-Empty": {"Sec-Fetch-Site": ""},
		"Sec-Fetch-Mode-Empty": {"Sec-Fetch-Mode": ""},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			h, _ := newTestHandler()
			rec := rpcReq(ctxWithPeerCred(501, true), h, "unix", "application/json", headers)
			assertForbiddenBody(t, rec, browserRefusalBody)
		})
	}
}

func TestRPCRefusesTextPlainPost(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	rec := rpcReq(ctxWithPeerCred(501, true), h, "unix", "text/plain", nil)
	assertForbiddenBody(t, rec, browserRefusalBody)
}

func TestRPCRefusesMissingContentType(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	rec := rpcReq(ctxWithPeerCred(501, true), h, "unix", "", nil)
	assertForbiddenBody(t, rec, browserRefusalBody)
}

func TestRPCRefusesForeignHost(t *testing.T) {
	withOwnerUID(t, 501)
	for _, host := range []string{"evil.example:7000", "localhost:7000"} {
		t.Run(host, func(t *testing.T) {
			h, _ := newTestHandler()
			rec := rpcReq(ctxWithPeerCred(501, true), h, host, "application/json", nil)
			assertForbiddenBody(t, rec, browserRefusalBody)
		})
	}
}

func TestEventsRefusesForeignHost(t *testing.T) {
	withOwnerUID(t, 501)
	h := newTestEventsHandler(t)

	rec := eventsReq(ctxWithPeerCred(501, true), h, "evil.example:7000", nil)
	assertForbiddenBody(t, rec, browserRefusalBody)
}

// TestRPCRefusalPrecedesParse proves a malformed body carrying a
// browser-shape marker (Origin) is refused with a plain 403, never the
// JSON-RPC parse-error envelope Parse would otherwise produce — the guard
// runs before Parse is ever called.
func TestRPCRefusalPrecedesParse(t *testing.T) {
	withOwnerUID(t, 501)
	h, _ := newTestHandler()

	req := httptest.NewRequest("POST", RPCPath, strings.NewReader("{not json")).WithContext(ctxWithPeerCred(501, true))
	req.Host = "unix"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assertForbiddenBody(t, rec, browserRefusalBody)
	if strings.Contains(rec.Body.String(), "jsonrpc") {
		t.Fatalf("body = %q, must be the plain 403 refusal, never a JSON-RPC envelope", rec.Body.String())
	}
}

// TestEventsRefusedBeforeSubscribe proves a refused GET /events never
// reaches SSEHandler.ServeHTTP at all: it runs the real SSEHandler over a
// real Bus (newTestEventsHandler), fires the refused request with a
// bounded timeout, and asserts both a fast return and no SSE prelude
// header. If guardLocalRequest were bypassed, the request would reach
// SSEHandler's stream loop, which blocks on an uncanceled context until an
// event or heartbeat — the bounded select below would then time out,
// failing this test. The bus package exposes no subscriber-count/
// equivalent observable to assert against directly (Subscribe's
// bookkeeping is package-private, and adding one is out of this ticket's
// files_scope); this timing-plus-no-SSE-header proof is the mechanism
// available without changing internal/events.
func TestEventsRefusedBeforeSubscribe(t *testing.T) {
	withOwnerUID(t, 501)
	h := newTestEventsHandler(t)

	req := httptest.NewRequest("GET", EventsPath, nil).WithContext(ctxWithPeerCred(999, true))
	req.Host = "unix"
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("refused GET /events must return immediately; it reached the SSE subscribe/stream loop instead")
	}
	if got := strings.TrimSpace(rec.Body.String()); got != ownerRefusalBody {
		t.Fatalf("body = %q, want %q", got, ownerRefusalBody)
	}
	if got := rec.Header().Get("Content-Type"); got == "text/event-stream" {
		t.Fatalf("Content-Type = %q, a refused request must never start the SSE handshake", got)
	}
}

// TestRPCAcceptsSocketHostsWithJSON is the positive control: an owner peer
// sending either accepted Host value with a JSON content type (parameters
// such as charset accepted) is served, not refused, on POST RPCPath — and
// its response is the real dispatched result, not merely "not a 403".
func TestRPCAcceptsSocketHostsWithJSON(t *testing.T) {
	withOwnerUID(t, 501)
	for _, tc := range []struct{ host, contentType string }{
		{"unix", "application/json"},
		{"cascade.sock", "application/json; charset=utf-8"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			h, reg := newTestHandler()
			reg.Register("m", func(context.Context, json.RawMessage) (any, error) { return "ok", nil })
			rec := rpcReq(ctxWithPeerCred(501, true), h, tc.host, tc.contentType, nil)
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
			}
			var env ResponseEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("unmarshal response: %v; body = %q", err, rec.Body.String())
			}
			if env.Error != nil {
				t.Fatalf("Error = %+v, want no error", env.Error)
			}
			if env.Result != "ok" {
				t.Fatalf("Result = %v, want %q", env.Result, "ok")
			}
		})
	}
}
