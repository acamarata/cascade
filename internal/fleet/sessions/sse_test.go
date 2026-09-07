// Purpose: SSEHandler's required unit tests, driving a REAL
//
//	internal/events.Bus (Art.2): prelude headers, wire-format fields
//	(event/data/id/retry), topic validation, heartbeat, and clean
//	disconnect (no goroutine/subscription leak). Only "net/http/httptest"
//	is imported, never "net/http" itself — the no-network-unit-lane gate
//	(internal/build/hygiene.go) bans a bare "net"/"net/http" import in a
//	non-integration _test.go file, mirroring internal/rpc/sse_test.go's
//	own established pattern (this ticket's brief names it explicitly).
//	The REAL independent-SSE-client conformance evidence lives in
//	sse_integration_test.go (behind the integration build tag); this file
//	is secondary/unit-level coverage plus the goroutine-leak proof.
//
// SPORT: internal.fleet.sessions.SSEHandler/ADDED (P1-E12-W3-S24-T3).
package sessions_test

import (
	"context"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

// syncRecorder mirrors internal/rpc/sse_test.go's own helper: ServeHTTP
// runs in a background goroutine while the test polls the growing body,
// so every access to the embedded recorder's unguarded fields needs a
// lock.
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

func newTestSSEHandler(t *testing.T) (*sessions.SSEHandler, *events.Bus, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })
	return sessions.NewSSEHandler(bus, clock), bus, clock
}

func runSSE(ctx context.Context, h *sessions.SSEHandler, query string) (*syncRecorder, <-chan struct{}) {
	w := newSyncRecorder()
	req := httptest.NewRequest("GET", sessions.EventsPath+query, nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, req)
		close(done)
	}()
	return w, done
}

func TestSessionSSE_Prelude_Headers(t *testing.T) {
	h, _, _ := newTestSSEHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "")
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

func TestSessionSSE_WireFormat_EventDataIDRetry(t *testing.T) {
	h, bus, _ := newTestSSEHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "?topic=fleet.sessions")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	if _, err := bus.Publish(ctx, "fleet.sessions", "fleet.sessions.changed", "test", []byte(`{"session_id":"s1"}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, "session_id") })
	cancel()
	<-done

	_, body, _ := w.snapshot()
	for _, field := range []string{"event: fleet.sessions.changed\n", "id: 1\n", `data: {"session_id":"s1"}` + "\n", "retry: 3000\n"} {
		if !strings.Contains(body, field) {
			t.Errorf("body missing field %q; got: %q", field, body)
		}
	}
	if !strings.HasSuffix(strings.TrimRight(body, " "), "\n\n") && !strings.Contains(body, "\n\n") {
		t.Errorf("SSE record must be terminated by a blank line; got: %q", body)
	}
}

// NOTE: ServeHTTP's "streaming not supported" (!canFlush) branch is not
// unit-tested here: proving it needs a http.ResponseWriter that does NOT
// satisfy http.Flusher, which requires naming the http.ResponseWriter
// type — an import of "net/http" itself, banned in a non-integration
// _test.go file by the no-network-unit-lane gate (this file may only
// import "net/http/httptest", per its own package doc above). That
// branch is a one-line defensive refusal with no observable behavior
// beyond the status code; internal/rpc/sse_test.go carries the identical
// gap for the same reason.

func TestSessionSSE_SubscribeError_500(t *testing.T) {
	h, bus, _ := newTestSSEHandler(t)
	if err := bus.Close(); err != nil {
		t.Fatalf("bus.Close: %v", err)
	}
	w := newSyncRecorder()
	req := httptest.NewRequest("GET", sessions.EventsPath, nil)
	h.ServeHTTP(w, req)
	status, body, _ := w.snapshot()
	if status != 500 {
		t.Fatalf("status = %d, want 500 when Subscribe fails on a closed bus", status)
	}
	if !strings.Contains(body, "failed to subscribe") {
		t.Fatalf("body = %q, want it to name the subscribe failure", body)
	}
}

func TestSessionSSE_UnknownTopic_400BeforeHandshake(t *testing.T) {
	h, _, _ := newTestSSEHandler(t)
	w, done := runSSE(context.Background(), h, "?topic=bogus.topic")
	<-done

	status, body, _ := w.snapshot()
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
	if !strings.Contains(body, "bogus.topic") {
		t.Fatalf("body must name the rejected topic, got: %s", body)
	}
	if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatal("SSE handshake must not have started for a rejected topic")
	}
}

func TestSessionSSE_Heartbeat_AfterClockAdvance(t *testing.T) {
	h, _, clock := newTestSSEHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	clock.Advance(16 * time.Second)
	waitFor(t, func() bool { _, body, _ := w.snapshot(); return strings.Contains(body, ": keep-alive\n\n") })

	cancel()
	<-done
}

// TestSessionSSE_ClientDisconnect_CleanUnsubscribeNoLeak proves ServeHTTP
// returns (and so its Unsubscribe has fully stopped the delivery
// goroutine — Bus.Unsubscribe blocks until it has) once the request
// context is canceled, with no dangling goroutine or subscription.
func TestSessionSSE_ClientDisconnect_CleanUnsubscribeNoLeak(t *testing.T) {
	h, bus, _ := newTestSSEHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after context cancel — goroutine leak")
	}

	// A fresh subscribe under the SAME cursor name space must succeed
	// immediately — if the prior subscription had leaked, a namespace
	// collision or blocked delivery goroutine would still be present.
	sub, err := bus.Subscribe(context.Background(), "fleet.sessions", "post-disconnect-probe", 1)
	if err != nil {
		t.Fatalf("Subscribe after disconnect: %v", err)
	}
	_ = sub.Unsubscribe()
}

// TestSessionSSE_Windows_Returns501 proves the handler actually refuses
// on a Windows CI lane (R-14.131); self-skips off-Windows since this
// ticket's files_scope names exactly sse_test.go, with no sibling
// `//go:build windows` file such as internal/rpc/sse_windows_test.go's.
func TestSessionSSE_Windows_Returns501(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("this refusal is GOOS-gated (sse.go); only Windows CI actually exercises it")
	}
	h, _, _ := newTestSSEHandler(t)
	req := httptest.NewRequest("GET", sessions.EventsPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 501 {
		t.Fatalf("status = %d, want 501 on Windows", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Windows") {
		t.Fatalf("501 body must be an actionable Windows-specific message, got: %q", rec.Body.String())
	}
}

func TestSessionSSE_ContextCancel_TerminatesCleanly(t *testing.T) {
	h, _, _ := newTestSSEHandler(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, done := runSSE(ctx, h, "")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not terminate on context deadline")
	}
}
