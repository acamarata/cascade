//go:build windows

package rpc

// Purpose: proves GET /events actually returns HTTP 501 on the Windows CI
//   lane (R-14.131: a platform-specific behavior needs a test that RUNS on
//   that platform, not merely builds), per this ticket's contract: "daemon
//   IPC unix socket is not supported on Windows." Only "net/http/httptest"
//   is imported, never "net/http" itself, per the no-network-unit-lane
//   gate (internal/build/hygiene.go) — see sse_test.go's constraint note.

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

func TestSSEHandler_Windows_Returns501(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	t.Cleanup(func() { _ = bus.Close() })

	h := NewSSEHandler(bus, "ns", func(events.EventKind) bool { return true }, clock)

	req := httptest.NewRequest("GET", EventsPath, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 501 {
		t.Fatalf("status = %d, want 501 on Windows", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Windows") {
		t.Fatalf("501 body must be an actionable Windows-specific message, got: %q", body)
	}
}
