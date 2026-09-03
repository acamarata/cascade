package rpc

// Purpose: SSEHandler's required unit tests, all driving a REAL
//   internal/events.Bus (Art.2): filter accept/reject, resume/invalid
//   token, heartbeat, clean disconnect (no leak), prelude headers, and one
//   W3C SSE fixture (external_contract).
// Constraints: only "net/http/httptest" is imported, never "net/http"
//   itself — the no-network-unit-lane gate (internal/build/hygiene.go)
//   bans a bare "net"/"net/http" import in a non-integration _test.go
//   file; httptest touches no real socket, so status codes below are
//   literal ints/strings (200, "GET", ...) rather than http.StatusOK/
//   http.MethodGet, matching handler_test.go's own established pattern.

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

const (
	kindA = events.EventKind("type.a")
	kindB = events.EventKind("type.b")
)

func knownAB(k events.EventKind) bool { return k == kindA || k == kindB }

// syncRecorder is a concurrency-safe wrapper around httptest.ResponseRecorder:
// ServeHTTP runs in its own goroutine while the test reads the growing
// body, so the recorder's unguarded Code/Body need a lock around every
// access. Header()/Flush() are promoted from the embedded recorder
// unchanged (see the constraint note above for why nothing here spells
// out "http.Header" or "http.Flusher" directly). wrote tracks whether
// WriteHeader has actually run — httptest.NewRecorder() pre-sets Code to
// 200 at construction, before any real write, so "wait for Code == 200"
// alone would report done instantly and race the handler; tests must wait
// for wrote instead.
type syncRecorder struct {
	mu    sync.Mutex
	wrote bool
	*httptest.ResponseRecorder
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (w *syncRecorder) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ResponseRecorder.Write(p)
}

func (w *syncRecorder) WriteHeader(status int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.wrote = true
	w.ResponseRecorder.WriteHeader(status)
}

// snapshot returns the response code, body so far, and whether
// WriteHeader has actually run.
func (w *syncRecorder) snapshot() (int, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Code, w.Body.String(), w.wrote
}

// waitFor polls cond every 5ms up to a bounded 2s deadline.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// newTestBus builds a real Bus over an in-memory Store with a clock.
func newTestBus() (*events.Bus, *testkit.FrozenClock) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	return events.New(storetest.NewMemStore(), clock), clock
}

// newTestSSEHandler builds an SSEHandler over a fresh real Bus.
func newTestSSEHandler(t *testing.T) (*SSEHandler, *events.Bus, *testkit.FrozenClock) {
	t.Helper()
	bus, clock := newTestBus()
	t.Cleanup(func() { _ = bus.Close() })
	return NewSSEHandler(bus, "ns", knownAB, clock), bus, clock
}

// runSSE drives h.ServeHTTP in a background goroutine, returning the
// recorder and a channel closed when ServeHTTP returns — stream's deferred
// sub.Unsubscribe() blocks until the delivery goroutine fully stops, so a
// closed done channel proves that goroutine already exited.
func runSSE(ctx context.Context, h *SSEHandler, query, lastEventID string) (*syncRecorder, <-chan struct{}) {
	w := newSyncRecorder()
	req := httptest.NewRequest("GET", EventsPath+query, nil).WithContext(ctx)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, req)
		close(done)
	}()
	return w, done
}

func TestSSEHandler_FilterValid_OnlyMatchingEventsArrive(t *testing.T) {
	h, bus, _ := newTestSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "?filter=type.a", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	mustPublish(t, bus, "ns", kindA, "1")
	mustPublish(t, bus, "ns", kindB, "2")
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, `"kind":"type.a"`) })

	cancel()
	<-done
	_, body, _ := w.snapshot()
	if strings.Contains(body, `"kind":"type.b"`) {
		t.Fatalf("filter=type.a must exclude type.b events, got body: %s", body)
	}
}

func TestSSEHandler_FilterUnknown_400BeforeHandshake(t *testing.T) {
	h, _, _ := newTestSSEHandler(t)

	w, done := runSSE(context.Background(), h, "?filter=type.a,bogus.type", "")
	<-done

	status, body, _ := w.snapshot()
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
	if !strings.Contains(body, "bogus.type") {
		t.Fatalf("body must name the rejected type, got: %s", body)
	}
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("SSE handshake must not have started for a rejected filter")
	}
	if strings.Contains(body, "data:") {
		t.Fatal("no SSE prelude/data may be written for a rejected filter")
	}
}

func TestSSEHandler_ResumeFromCursor_ReplaysFromPosition(t *testing.T) {
	h, bus, _ := newTestSSEHandler(t)

	ev1 := mustPublish(t, bus, "ns", kindA, "1")
	mustPublish(t, bus, "ns", kindA, "2")

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", formatResumeToken(ev1.Seq))
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, `"seq":2`) })
	cancel()
	<-done

	_, body, _ := w.snapshot()
	if strings.Contains(body, `"seq":1,`) {
		t.Fatalf("resume from seq %d must not redeliver seq 1, got: %s", ev1.Seq, body)
	}
}

func TestSSEHandler_ResumeInvalidToken_OpensAtTail(t *testing.T) {
	h, bus, _ := newTestSSEHandler(t)
	// Pre-existing events (currentTailSeq must snapshot a real, non-zero
	// tail across more than one already-persisted event) that a garbage
	// Last-Event-ID must NOT replay.
	mustPublish(t, bus, "ns", kindA, "1")
	mustPublish(t, bus, "ns", kindA, "2")

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", "%%%not-valid-base64%%%")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
	mustPublish(t, bus, "ns", kindA, "3")
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, `"seq":3`) })
	cancel()
	<-done

	status, body, _ := w.snapshot()
	if status != 200 {
		t.Fatalf("garbage Last-Event-ID must still succeed, got status %d", status)
	}
	if strings.Contains(body, `"seq":1,`) || strings.Contains(body, `"seq":2,`) {
		t.Fatalf("garbage Last-Event-ID must open at tail, not replay pre-existing events: %s", body)
	}
}

func TestSSEHandler_Heartbeat_AfterClockAdvance(t *testing.T) {
	h, _, clock := newTestSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	clock.Advance(16 * time.Second)
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, ": keep-alive\n\n") })

	cancel()
	<-done
}

func TestSSEHandler_ClientDisconnect_CleanUnsubscribeNoLeak(t *testing.T) {
	h, _, _ := newTestSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancel — goroutine leak")
	}

	// done closing already proves no leak: Unsubscribe blocks until the
	// delivery goroutine fully stops.
}

func TestSSEHandler_Prelude_Headers(t *testing.T) {
	h, _, _ := newTestSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
	cancel()
	<-done

	want := map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "Connection": "keep-alive"}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// TestSSEHandler_WireFormat_MatchesW3CSSESpec is the external_contract
// test: the W3C SSE Living Standard (captured 2026-09-03) defines an
// event as "id"/"data" fields, each "<field>: <value>" on its own line,
// terminated by one blank line — asserted against the actual wire bytes.
func TestSSEHandler_WireFormat_MatchesW3CSSESpec(t *testing.T) {
	h, bus, _ := newTestSSEHandler(t)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
	mustPublish(t, bus, "ns", kindA, "src")
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, "data:") })
	cancel()
	<-done

	_, body, _ := w.snapshot()
	eventRecord := regexp.MustCompile(`^id: [A-Za-z0-9_-]+\ndata: .+\n\n`)
	if !eventRecord.MatchString(body) {
		t.Fatalf("wire output does not match W3C SSE id/data record grammar: %q", body)
	}
}

func mustPublish(t *testing.T, bus *events.Bus, ns string, kind events.EventKind, source string) events.Event {
	t.Helper()
	ev, err := bus.Publish(context.Background(), ns, kind, source, []byte(source))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return ev
}

func TestSSEHandler_SubscribeError_500(t *testing.T) {
	bus, clock := newTestBus()
	_ = bus.Close() // Subscribe refuses once closed (cascade.KindUnavailable)
	h := NewSSEHandler(bus, "ns", knownAB, clock)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", EventsPath, nil))
	if rec.Code != 500 {
		t.Fatalf("status = %d, want 500 when Subscribe fails", rec.Code)
	}
}
