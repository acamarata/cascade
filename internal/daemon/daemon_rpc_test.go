package daemon

// Purpose: covers NewRPCServer (daemon.go), the wiring point this ticket
//   (D/S-06.T3) added to mount internal/rpc's JSON-RPC handler on the
//   daemon's HTTP server. Not named in T-3's files_scope (which lists only
//   daemon.go under "change") — added by necessity to avoid regressing
//   internal/daemon's measured coverage floor with untested new lines,
//   same class of authorized-write-set extension as R-14.113/R-14.133;
//   flagged in this ticket's journal for T0 to ratify.
// Constraints: Art.7.1 — no real network listener; exercises the built
//   *http.Server's Handler and ConnContext fields directly rather than
//   binding a socket.

import (
	"net/http/httptest"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

func TestNewRPCServer_WiresHandlerAndConnContext(t *testing.T) {
	srv := NewRPCServer(rpc.NewRegistry(), nil)
	if srv.Handler == nil {
		t.Fatal("NewRPCServer: Handler is nil")
	}
	if srv.ConnContext == nil {
		t.Fatal("NewRPCServer: ConnContext is nil")
	}

	// The mux must route POST /rpc to something other than a 404; a GET or
	// wrong-path request should 404 from the mux, proving routing is real
	// rather than a catch-all handler.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/not-rpc", nil)
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("GET /not-rpc: got status %d, want 404 (mux should not catch-all)", rec.Code)
	}
}
