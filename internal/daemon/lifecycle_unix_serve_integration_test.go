//go:build !windows && integration

package daemon

// Purpose: real-socket coverage for lifecycle_unix_serve.go's three
//   pieces under the http.Server hand-off: drainRefusingListener actually
//   refuses a connection made while draining, preserved across the
//   hand-off; serveRPC's ConnState wiring tracks the true open-connection
//   count; and shutdownRPCServer never leaves a connection open past its
//   call. Needs "net", so this lives in the `-tags=integration` lane
//   alongside this package's other real-socket tests.
// SPORT: internal/daemon (ADD).

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestDrainRefusingListener_RefusesConnectionWhileDraining dials a real
// connection against a listener already mid-Drain and proves the server
// side closes it without ever handing it to the wrapped http.Server.Serve
// caller: the client's Read returns an error (EOF/closed), not data or a
// hang.
func TestDrainRefusingListener_RefusesConnectionWhileDraining(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	mgr := NewUpgradeManager(runtime.NewSystemClock(), func(time.Duration) {}, nil, nil, nil)
	if err := mgr.Drain(context.Background(), nil, nil, 0); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	wrapped := &drainRefusingListener{Listener: ln, upgrade: mgr}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		_, _ = wrapped.Accept() // blocks refusing connections until ln closes
	}()

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, readErr := conn.Read(buf); readErr == nil {
		t.Fatal("connection made while draining was not refused: got data, want the server to have closed it")
	}

	_ = ln.Close()
	<-acceptDone
}

// TestServeRPC_TracksActiveConnectionsAcrossConnState proves active
// reflects the true open-connection count via ConnState, incrementing on
// connect and decrementing once the connection is actually closed —
// accurate for a real dialed connection, not just an Accept-then-
// immediately-close placeholder.
func TestServeRPC_TracksActiveConnectionsAcrossConnState(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler()}
	var active int64
	serveDone := serveRPC(ln, srv, &active)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&active) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt64(&active) != 1 {
		t.Fatalf("active = %d after dial, want 1", atomic.LoadInt64(&active))
	}

	_ = conn.Close()
	deadline = time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&active) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(&active); got != 0 {
		t.Fatalf("active = %d after client close, want 0", got)
	}

	_ = srv.Close()
	<-serveDone
}

// TestShutdownRPCServer_ForceClosesRemainingConnections proves HARD
// REQUIREMENT 3: a connection still open when grace elapses is force-
// closed, never leaked. It dials a connection and never closes it itself
// client-side; shutdownRPCServer alone must close it.
func TestShutdownRPCServer_ForceClosesRemainingConnections(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: http.NotFoundHandler()}
	var active int64
	serveDone := serveRPC(ln, srv, &active)

	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(5 * time.Second)
	for atomic.LoadInt64(&active) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	shutdownRPCServer(srv, 50*time.Millisecond) // client never closes its side
	<-serveDone

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	if _, readErr := conn.Read(buf); readErr == nil {
		t.Fatal("connection still open after shutdownRPCServer: want it force-closed")
	}
}
