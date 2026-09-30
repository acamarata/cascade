package rpc

// Purpose: SSEMux's required tests (P1-E12-W6-S121-T1): no key naming a
//   topic reaches Default, ignoring an unrelated key; exactly one
//   registered topic reaches that handler and no other; an unknown,
//   case-variant, padded, empty, traversal-shaped, malformed, repeated or
//   smuggled topic key is refused with HTTP 400 and a fixed JSON-RPC
//   KindInvalidInput message that never echoes the query; a non-owner or
//   browser-shaped request is refused with HTTP 403 before all of that
//   (R-14.312); an event-visibility predicate installed on the request
//   context reaches the topic handler unchanged.
//
// Constraints: only "net/http/httptest" is imported, never "net/http":
//   internal/build's no-network-unit-lane gate bans a bare "net"/"net/http"
//   import in a non-integration _test.go file. Dispatch is distinguished
//   with REAL *SSEHandler instances, each accepting only its own filter
//   kind, so which handler ran is provable from its status code alone. The
//   one recording handler (recordingTopic) is generic over its parameter
//   types, instantiated from (*SSEMux).ServeHTTP's own signature, so it
//   implements http.Handler without this file naming net/http types.
//
// SPORT: internal.rpc.SSEMux/ADDED (P1-E12-W6-S121-T1).

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// sseMuxTestFixture builds a real bus plus three real *SSEHandler
// instances over distinct namespaces, each accepting only its own
// eponymous filter kind.
func sseMuxTestFixture(t *testing.T) *SSEMux {
	t.Helper()
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	only := func(kind events.EventKind) KnownEventKind {
		return func(k events.EventKind) bool { return k == kind }
	}
	def := NewSSEHandler(bus, "default-ns", only("default-kind"), clock)
	sessionsHandler := NewSSEHandler(bus, "sessions-ns", only("sessions-kind"), clock)
	otherHandler := NewSSEHandler(bus, "other-ns", only("other-kind"), clock)
	return NewSSEMux(def).WithTopic("fleet.sessions", sessionsHandler).WithTopic("fleet.other", otherHandler)
}

// serveMux drives mux.ServeHTTP against path as an authorized owner peer
// (Host "unix") on ctx derived from base, bounded to 100ms so a successful
// SSE handshake's stream loop returns on ctx.Done(); a 4xx returns long
// before the loop is reached.
func serveMux(base context.Context, t *testing.T, mux *SSEMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	withOwnerUID(t, 501)
	ctx, cancel := context.WithTimeout(base, 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	req.Host = "unix"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func serveMuxShortLived(t *testing.T, mux *SSEMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	return serveMux(ctxWithPeerCred(501, true), t, mux, path)
}

func TestSSEMuxDefaultWithoutTopic(t *testing.T) {
	// filter=default-kind is known only to Default: 200 proves Default ran.
	rec := serveMuxShortLived(t, sseMuxTestFixture(t), EventsPath+"?filter=default-kind")
	if rec.Code != 200 {
		t.Fatalf("no-topic request: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
}

func TestSSEMuxDispatchesByTopic(t *testing.T) {
	mux := sseMuxTestFixture(t)
	rec := serveMuxShortLived(t, mux, EventsPath+"?topic=fleet.sessions&filter=sessions-kind")
	if rec.Code != 200 {
		t.Fatalf("topic=fleet.sessions: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	// The same filter against a different topic must 400 from THAT
	// handler: dispatch did not leak to the sessions handler or Default.
	rec = serveMuxShortLived(t, mux, EventsPath+"?topic=fleet.other&filter=sessions-kind")
	if rec.Code != 400 {
		t.Fatalf("topic=fleet.other with the sessions filter: status = %d, want 400", rec.Code)
	}
}

// TestSSEMuxUnknownTopicRefuses: every topic shape other than exactly one
// registered, byte-exact value gets the mux's own JSON 400 (a delegate
// handler's filter 400 is text/plain), never 200. Each case carries no
// filter, so a request that slipped through to any handler would get 200.
func TestSSEMuxUnknownTopicRefuses(t *testing.T) {
	mux := sseMuxTestFixture(t)
	cases := map[string]string{
		"unknown":              "topic=nonexistent.topic",
		"case variant":         "topic=Fleet.Sessions",
		"trailing space":       "topic=fleet.sessions%20",
		"empty value":          "topic=",
		"traversal":            "topic=../fleet.sessions",
		"repeated, real first": "topic=fleet.sessions&topic=x",
		"repeated, real last":  "topic=x&topic=fleet.sessions",
		"repeated, both real":  "topic=fleet.sessions&topic=fleet.sessions",
		"malformed escape":     "topic=fleet.sessions&topic=%zz",
		"semicolon smuggle":    "topic=fleet.sessions;topic=x",
		// A key that only LOOKS like "topic" (wrong case, or
		// padded by a "+"/"%20" prefix decoding to a leading space)
		// must never fall through to Default like "filter" does.
		"capitalized key":      "Topic=fleet.sessions",
		"all-caps key":         "TOPIC=fleet.sessions",
		"unknown, capital key": "Topic=unknown",
		"plus-padded key":      "+topic=fleet.sessions",
		"percent-padded key":   "%20topic=fleet.sessions",
		// A second key folding to "topic" alongside the real one
		// is smuggling, both orders, never "first wins".
		"real key plus case-variant key": "topic=fleet.sessions&Topic=other",
		"case-variant key plus real key": "Topic=other&topic=fleet.sessions",
	}
	for name, query := range cases {
		t.Run(name, func(t *testing.T) {
			rec := serveMuxShortLived(t, mux, EventsPath+"?"+query)
			if rec.Code != 400 {
				t.Fatalf("%s: status = %d, want 400", query, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("%s: Content-Type = %q, want application/json", query, got)
			}
			var env ResponseEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("%s: body is not a JSON-RPC envelope: %v (%q)", query, err, rec.Body.String())
			}
			if env.Error == nil || env.Error.Code != cascade.KindInvalidInput.JSONRPCCode() || env.Error.Message == "" {
				t.Fatalf("%s: envelope error = %+v, want KindInvalidInput with a message", query, env.Error)
			}
		})
	}
}

// TestSSEMuxDefaultIgnoresUnrelatedQueryKeys: no key naming a topic still
// serves Default despite an unrelated one (Default's "filter", or a
// caller's "job_id") — the shape run_exec.go dials in production. The
// refusal above is about keys that fold to "topic", never an unrelated one.
func TestSSEMuxDefaultIgnoresUnrelatedQueryKeys(t *testing.T) {
	for _, query := range []string{"filter=default-kind", "job_id=abc"} {
		if rec := serveMuxShortLived(t, sseMuxTestFixture(t), EventsPath+"?"+query); rec.Code != 200 {
			t.Fatalf("%s: status = %d, want 200 (Default)", query, rec.Code)
		}
	}
}

// TestSSEMuxTopicRefusalNeverEchoesInput: an unknown topic's 400 message
// is the fixed string "unknown /events topic", byte for byte, never that
// plus the attacker's value in any encoding (a raw Contains check alone
// would miss a JSON-escaped echo of the planted ANSI escape, so this
// checks the decoded message exactly).
func TestSSEMuxTopicRefusalNeverEchoesInput(t *testing.T) {
	mux := sseMuxTestFixture(t)
	for name, topic := range map[string]string{
		"ansi escape": "\x1b[2J\x1b[H",
		"huge topic":  strings.Repeat("A", 200*1024),
	} {
		t.Run(name, func(t *testing.T) {
			rec := serveMuxShortLived(t, mux, EventsPath+"?topic="+url.QueryEscape(topic))
			body := rec.Body.Bytes()
			if rec.Code != 400 || len(body) > 4096 {
				t.Fatalf("status = %d, body = %d bytes; want 400 and a small fixed-size body", rec.Code, len(body))
			}
			var env ResponseEnvelope
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("body is not a JSON-RPC envelope: %v", err)
			}
			if env.Error == nil || env.Error.Message != "unknown /events topic" {
				t.Fatalf("message = %+v, want the fixed string \"unknown /events topic\" (no echoed topic)", env.Error)
			}
		})
	}
}

// serveMuxRefused drives a topic=fleet.sessions request expected to be
// refused by guardLocalRequest, with a bounded wait: a bypassed guard
// would reach the real sessions handler's stream loop, which blocks on
// this uncanceled context, so the select times out instead of returning.
func serveMuxRefused(ctx context.Context, t *testing.T, mux *SSEMux, extra map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", EventsPath+"?topic=fleet.sessions", nil).WithContext(ctx)
	req.Host = "unix"
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { mux.ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("refused topic stream must return immediately; it reached the delegate handler's stream loop")
	}
	return rec
}

// TestSSEMuxRefusesNonOwnerPeer: a resolved non-owner peer, and a peer
// whose credentials never resolved, get request_guard.go's exact owner
// 403 before topic dispatch, and no SSE handshake starts.
func TestSSEMuxRefusesNonOwnerPeer(t *testing.T) {
	withOwnerUID(t, 501)
	mux := sseMuxTestFixture(t)
	for _, ctx := range []context.Context{ctxWithPeerCred(999, true), ctxWithPeerCred(501, false)} {
		rec := serveMuxRefused(ctx, t, mux, nil)
		assertForbiddenBody(t, rec, ownerRefusalBody)
		if got := rec.Header().Get("Content-Type"); got == "text/event-stream" {
			t.Fatalf("Content-Type = %q, a refused request must never start the SSE handshake", got)
		}
	}
}

// TestSSEMuxRefusesOriginHeader: an owner peer whose request carries an
// Origin or a Sec-Fetch-Site header (present at all, any value) gets the
// browser-shape 403 before topic dispatch.
func TestSSEMuxRefusesOriginHeader(t *testing.T) {
	withOwnerUID(t, 501)
	mux := sseMuxTestFixture(t)
	for _, extra := range []map[string]string{
		{"Origin": "http://evil.example"},
		{"Origin": "null"},
		{"Sec-Fetch-Site": "cross-site"},
	} {
		rec := serveMuxRefused(ctxWithPeerCred(501, true), t, mux, extra)
		assertForbiddenBody(t, rec, browserRefusalBody)
	}
}

// recordingTopic is a topic handler that records the request context it
// was served with. W and R are instantiated from (*SSEMux).ServeHTTP's
// signature by newRecordingTopic, so recordingTopic[W, R] has exactly
// http.Handler's method set.
type recordingTopic[W, R any] struct{ seen *context.Context }

func (h recordingTopic[W, R]) ServeHTTP(_ W, r R) {
	*h.seen = any(r).(interface{ Context() context.Context }).Context()
}

func newRecordingTopic[M, W, R any](_ func(M, W, R), seen *context.Context) recordingTopic[W, R] {
	return recordingTopic[W, R]{seen: seen}
}

// TestSSEMuxEventVisiblePredicate: a predicate installed with
// WithEventVisible before ServeHTTP reaches the topic handler as the same
// function (pointer-equal, and it answers the same when called); with none
// installed EventVisibleFrom reports ok=false there; a nil predicate
// installs a hide-everything predicate, never "no filter".
func TestSSEMuxEventVisiblePredicate(t *testing.T) {
	var seen context.Context
	mux := sseMuxTestFixture(t).WithTopic("recording", newRecordingTopic((*SSEMux).ServeHTTP, &seen))
	pred := func(sessionID, parentSessionID string) bool { return sessionID == "visible" && parentSessionID == "" }

	rec := serveMux(WithEventVisible(ctxWithPeerCred(501, true), pred), t, mux, EventsPath+"?topic=recording")
	if rec.Code != 200 || seen == nil {
		t.Fatalf("topic=recording: status = %d, handler ran = %v; want 200 and the recording handler reached", rec.Code, seen != nil)
	}
	got, ok := EventVisibleFrom(seen)
	if !ok {
		t.Fatal("EventVisibleFrom(handler ctx) ok = false, want the installed predicate")
	}
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(pred).Pointer() {
		t.Fatal("handler received a different predicate function than the one installed")
	}
	if !got("visible", "") || got("hidden", "") || got("visible", "parent") {
		t.Fatal("handler's predicate does not answer like the installed one")
	}

	seen = nil
	serveMux(ctxWithPeerCred(501, true), t, mux, EventsPath+"?topic=recording")
	if seen == nil {
		t.Fatal("recording handler not reached without a predicate")
	}
	if _, ok := EventVisibleFrom(seen); ok {
		t.Fatal("EventVisibleFrom ok = true with no predicate installed, want false")
	}

	deny, ok := EventVisibleFrom(WithEventVisible(context.Background(), nil))
	if !ok || deny("visible", "") {
		t.Fatalf("WithEventVisible(nil): ok = %v; want a predicate that hides every event", ok)
	}
}
