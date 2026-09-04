//go:build integration

// Purpose: end-to-end tests dialing a REAL internal/rpc.SSEHandler over a
//
//	REAL unix socket, backed by a REAL internal/events.Bus (never a fake
//	event source), including a real resume-token round trip — Art.2's
//	"faithfully implements the real framing" requirement applied to the
//	SSE half of this SDK. Build-tagged "integration" for the same reason
//	as client_integration_test.go.
//
// SPORT: internal/client (ADD, per T-3 sport_updates).
package client

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

const testEventKind = events.EventKind("client_test.event")

func knownTestEventKind(k events.EventKind) bool { return k == testEventKind }

// startRealSSESocket builds a real events.Bus and serves it through the
// REAL rpc.NewHandlerWithSSE(registry, sse) pipeline over a real unix
// socket, mirroring cmd/cascade/daemon_unix_run.go's buildRPCServer
// wiring for GET /events.
func startRealSSESocket(t *testing.T) (sockPath string, bus *events.Bus, dial DialFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "streamit")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath = filepath.Join(dir, "d.sock")

	clock := runtime.NewSystemClock()
	bus = events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	sse := rpc.NewSSEHandler(bus, "client-test-ns", knownTestEventKind, clock)
	h := rpc.NewHandlerWithSSE(rpc.NewRegistry(), sse)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: h, ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	dial = func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}
	return sockPath, bus, dial
}

// TestStreamClient_Open_ReceivesRealEvent proves streamClient.open decodes
// a real event published on a real Bus and forwarded through a real
// rpc.SSEHandler — not a canned SSE body.
func TestStreamClient_Open_ReceivesRealEvent(t *testing.T) {
	sockPath, bus, dial := startRealSSESocket(t)
	sc := newStreamClient(sockPath, dial)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events_, closeFn, err := sc.open(ctx, "", "")
	if err != nil {
		t.Fatalf("open: unexpected error: %v", err)
	}
	defer closeFn()

	if _, pubErr := bus.Publish(ctx, "client-test-ns", testEventKind, "src", []byte("hello")); pubErr != nil {
		t.Fatalf("Publish: %v", pubErr)
	}

	select {
	case ev, ok := <-events_:
		if !ok {
			t.Fatal("event channel closed before an event arrived")
		}
		if ev.ID == "" {
			t.Error("event ID is empty, want a resume token")
		}
		if ev.Data == "" {
			t.Error("event Data is empty")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the published event")
	}
}

// TestStreamClient_Open_ResumeToken proves a second connection using the
// first event's ID as Last-Event-ID does NOT re-deliver that same event —
// the real resume-token semantics internal/rpc/sse.go implements.
func TestStreamClient_Open_ResumeToken(t *testing.T) {
	sockPath, bus, dial := startRealSSESocket(t)
	sc := newStreamClient(sockPath, dial)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	firstCh, firstClose, err := sc.open(ctx, "", "")
	if err != nil {
		t.Fatalf("open (first): %v", err)
	}
	if _, pubErr := bus.Publish(ctx, "client-test-ns", testEventKind, "src", []byte("one")); pubErr != nil {
		t.Fatalf("Publish: %v", pubErr)
	}
	var firstID string
	select {
	case ev := <-firstCh:
		firstID = ev.ID
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first event")
	}
	firstClose()

	if _, pubErr := bus.Publish(ctx, "client-test-ns", testEventKind, "src", []byte("two")); pubErr != nil {
		t.Fatalf("Publish: %v", pubErr)
	}

	secondCh, secondClose, err := sc.open(ctx, "", firstID)
	if err != nil {
		t.Fatalf("open (resumed): %v", err)
	}
	defer secondClose()

	select {
	case ev, ok := <-secondCh:
		if !ok {
			t.Fatal("resumed event channel closed with no event")
		}
		if ev.Data == "" {
			t.Error("resumed event Data is empty")
		}
		if ev.ID == firstID {
			t.Errorf("resumed stream re-delivered the first event (ID %q), want only events after it", firstID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the resumed event")
	}
}
