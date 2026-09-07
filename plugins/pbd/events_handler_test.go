// Purpose: real-behavior unit coverage for pbd.go's eventsHandler and
//
//	streamEvents (S-29.T1's SSE bridge): prelude headers, wire-format SSE
//	framing, method/path rejection, and clean unsubscribe on client
//	disconnect. Follows internal/fleet/sessions/sse_test.go's established
//	pattern verbatim: only "net/http/httptest" is imported, never
//	"net/http" itself, satisfying the no-network-unit-lane gate
//	(internal/build/hygiene.go bans a bare "net"/"net/http" import in a
//	non-integration _test.go file). The REAL independent-client
//	conformance evidence (curl against a real listener) lives in
//	projector_test.go, behind the integration build tag.
//
// Inputs: none (in-process bus + httptest recorder/request only).
// Outputs: none; asserts on recorded status/headers/body and bus state.
// Constraints: deterministic — every wait is bounded (waitFor's deadline
//
//	loop, mirroring sse_test.go's own helper, or a select with a
//	time.After bound), never an unbounded receive or a bare sleep used to
//	observe an event.
//
// SPORT: plugins/pbd.eventsHandler/COVERED (P1-E14-W3-S29-T1 coverage
// gap fix).
package pbd

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncRecorder mirrors internal/fleet/sessions/sse_test.go's own helper:
// ServeHTTP runs in a background goroutine while the test polls the
// growing body, so every access to the embedded recorder's unguarded
// fields needs a lock.
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

func (w *syncRecorder) snapshot() (int, string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Code, w.Body.String(), w.wrote
}

func waitForCond(t *testing.T, cond func() bool) {
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

func runEventsHandler(ctx context.Context, h *eventsHandler, path string) (*syncRecorder, <-chan struct{}) {
	w := newSyncRecorder()
	req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, req)
		close(done)
	}()
	return w, done
}

func TestEventsHandler_ServeHTTP_PreludeHeaders(t *testing.T) {
	h := &eventsHandler{bus: newBus()}
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runEventsHandler(ctx, h, eventsPath)
	waitForCond(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
	cancel()
	<-done

	want := map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "Connection": "keep-alive"}
	for k, v := range want {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	status, _, _ := w.snapshot()
	if status != 200 {
		t.Errorf("status = %d, want 200", status)
	}
}

func TestEventsHandler_ServeHTTP_WireFormat_IDAndData(t *testing.T) {
	b := newBus()
	h := &eventsHandler{bus: b}
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runEventsHandler(ctx, h, eventsPath)
	waitForCond(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	if err := b.Publish(ctx, "ticket.updated", []byte(`{"id":"T1"}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	waitForCond(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, "T1") })
	cancel()
	<-done

	_, body, _ := w.snapshot()
	for _, field := range []string{"id: ticket.updated\n", `data: {"id":"T1"}` + "\n"} {
		if !strings.Contains(body, field) {
			t.Errorf("body missing field %q; got: %q", field, body)
		}
	}
	if !strings.Contains(body, "\n\n") {
		t.Errorf("SSE record must be terminated by a blank line; got: %q", body)
	}
}

func TestEventsHandler_ServeHTTP_WrongPath404(t *testing.T) {
	h := &eventsHandler{bus: newBus()}
	w, done := runEventsHandler(context.Background(), h, "/not-events")
	<-done
	status, _, _ := w.snapshot()
	if status != 404 {
		t.Fatalf("status = %d, want 404 for the wrong path", status)
	}
}

func TestEventsHandler_ServeHTTP_WrongMethod404(t *testing.T) {
	h := &eventsHandler{bus: newBus()}
	w := newSyncRecorder()
	req := httptest.NewRequest("POST", eventsPath, nil)
	h.ServeHTTP(w, req)
	status, _, _ := w.snapshot()
	if status != 404 {
		t.Fatalf("status = %d, want 404 for a non-GET method", status)
	}
}

// TestEventsHandler_ServeHTTP_DisconnectUnsubscribes proves ServeHTTP
// returns once the request context is canceled (no goroutine left
// running streamEvents) and that its deferred unsubscribe actually
// removed the subscription from the bus, never leaking a subscriber
// entry.
func TestEventsHandler_ServeHTTP_DisconnectUnsubscribes(t *testing.T) {
	b := newBus()
	h := &eventsHandler{bus: b}
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runEventsHandler(ctx, h, eventsPath)
	waitForCond(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	b.mu.Lock()
	duringCount := len(b.subs)
	b.mu.Unlock()
	if duringCount != 1 {
		t.Fatalf("subs during connection = %d, want 1", duringCount)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancel — goroutine leak")
	}

	b.mu.Lock()
	afterCount := len(b.subs)
	b.mu.Unlock()
	if afterCount != 0 {
		t.Fatalf("subs after disconnect = %d, want 0 (unsubscribe leaked)", afterCount)
	}
}

// TestStreamEvents_ClosedChannel_ReturnsWithoutPublish proves the
// "!open" branch: an unsubscribe closing a connection's channel out
// from under a still-running streamEvents call must return cleanly,
// never block or panic on a closed-channel receive.
func TestStreamEvents_ClosedChannel_ReturnsWithoutPublish(t *testing.T) {
	w := newSyncRecorder()
	w.WriteHeader(200)
	ch := make(chan busEvent)
	close(ch)

	done := make(chan struct{})
	go func() {
		streamEvents(context.Background(), w, w.ResponseRecorder, ch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamEvents did not return on a closed channel")
	}
	_, body, _ := w.snapshot()
	if body != "" {
		t.Fatalf("body = %q, want empty (no event should have been written)", body)
	}
}

// TestStreamEvents_ContextCancel_TerminatesCleanly proves the ctx.Done
// branch independent of a real ServeHTTP connection.
func TestStreamEvents_ContextCancel_TerminatesCleanly(t *testing.T) {
	w := newSyncRecorder()
	w.WriteHeader(200)
	ch := make(chan busEvent)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		streamEvents(ctx, w, w.ResponseRecorder, ch)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamEvents did not terminate on context deadline")
	}
}
