//go:build !windows && integration

package daemon

// Purpose: TestStatusWidgetPeerUIDRefused (task 9's third proof) proves
// status.widget is dispatched only after the inherited D/S-06.T3
// socket-peer-UID check runs. Lives behind the `integration` build tag,
// NOT in status_widget_privacy_test.go, because it needs a real
// net.Conn — internal/build's TestNoNetworkUnitTest_RealTreeGreen gate
// (Art.7.2) mechanically forbids any untagged _test.go file from
// importing "net" or "net/http" at all (verified: this ticket hit that
// gate red with this exact test in the default lane before moving it
// here), mirroring internal/daemon/daemon_ipc_e2e_integration_test.go's
// own identical need for a real net primitive.
//
// This package cannot fabricate a resolved non-owner UID directly:
// internal/rpc's peerCred/peerCredKey/ownerUID seams are unexported by
// design (a security gate is not meant to be poked from outside its own
// package), and internal/rpc/handler_test.go's own
// TestHandler_UIDResolutionFailedRejected already proves the OTHER half
// of the same fail-closed OR (`!cred.ok || cred.uid != ownerUID()`): an
// unresolvable peer identity is refused exactly like a resolved wrong
// one. This test drives that same code path from outside the package via
// the EXPORTED rpc.ConnContext over a net.Pipe() connection — a real
// net.Conn, but purely in-memory (no OS socket, no listener):
// peerCredFromConn (handler_darwin.go/handler_linux.go) type-asserts for
// *net.UnixConn and reports ok=false for anything else, so this exercises
// the real production credential-resolution path, not a spoofed context
// field this package has no access to construct.
//
// CONTRACT NOTE: this ticket's own checks list runs
// `go test -run TestStatusWidgetPeerUIDRefused ./internal/daemon/ -v`
// with no `-tags=integration`. Run literally, that command builds this
// package WITHOUT this file (the tag excludes it) and reports 0 tests
// run — not a failure, but not a real proof either. The journal for this
// ticket quotes both the checks-list text and this gate's real,
// unconditional behavior, and records the actual verification command
// used: `go test -tags=integration -run TestStatusWidgetPeerUIDRefused
// ./internal/daemon/ -v`.
//
// SPORT: daemon.status_widget (ADD, P1-E38-W8-S74-T1).

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
)

func TestStatusWidgetPeerUIDRefused(t *testing.T) {
	reg, _ := setupStatusWidget(t, nil)
	handler := rpc.NewHandler(reg)

	clientConn, serverConn := net.Pipe()
	defer func() { _ = clientConn.Close() }()
	defer func() { _ = serverConn.Close() }()

	ctx := rpc.ConnContext(context.Background(), serverConn)
	body := `{"jsonrpc":"2.0","method":"` + MethodStatusWidget + `","id":1}`
	req := httptest.NewRequest(http.MethodPost, rpc.RPCPath, strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (peer credential unresolvable over a non-unix net.Conn, fail-closed)", rec.Code)
	}
}
