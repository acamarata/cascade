//go:build integration

// Purpose: end-to-end tests dialing a REAL internal/rpc.Registry/Handler
//
//	pipeline over a REAL unix socket — the exact server the daemon
//	composition root builds (cmd/cascade/daemon_unix_run.go's
//	buildRPCServer), not a self-authored fake that merely returns
//	canned bytes (Art.2's "faithfully implements the real method-dispatch
//	framing" requirement). Build-tagged "integration" because
//	internal/build's no-network-unit-lane gate (Art.7.2) forbids an
//	untagged _test.go file from importing "net"/"net/http" — see
//	client_test.go for this package's network-free unit tests.
//
// SPORT: internal/client (ADD, per T-3 sport_updates).
package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// startRealDaemonSocket builds the REAL internal/rpc.Registry/Handler
// pipeline with status.get registered exactly the way
// internal/daemon.StatusProvider does in production, serves it over a
// REAL unix socket, and returns a DialFunc reaching it. Mirrors
// cmd/cascade/daemon_unix_status_integration_test.go's own pattern.
func startRealDaemonSocket(t *testing.T) (socketPath string, dial DialFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "clientit")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	provider := daemon.NewStatusProvider(runtime.SystemClock{}, time.Now(), sockPath, nil, nil)
	registry := rpc.NewRegistry()
	registry.Register(daemon.StatusMethod, provider.Handler())

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: rpc.NewHandler(registry), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	return sockPath, func(ctx context.Context, path string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}
}

// TestClient_Status_RealSocket proves Status() is reachable and decodes a
// real, live status.get response over a real socket — PID matches this
// test process, matching daemon_unix_status_integration_test.go's own
// assertion shape for the raw-client precursor this ticket replaces.
func TestClient_Status_RealSocket(t *testing.T) {
	sockPath, dial := startRealDaemonSocket(t)
	c := New(sockPath, dial, 10*time.Second)

	res, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: unexpected error: %v", err)
	}
	if res.Daemon.PID != os.Getpid() {
		t.Errorf("Daemon.PID = %d, want this test process's pid %d", res.Daemon.PID, os.Getpid())
	}
	if res.Health != "ok" {
		t.Errorf("Health = %q, want %q", res.Health, "ok")
	}
	if res.Version == "" {
		t.Error("Version is empty")
	}
}

// TestClient_Do_UnknownMethod proves an unregistered method reaches the
// real registry's -32601 path and Do maps it to KindNotFound, over a real
// socket (not a canned response).
func TestClient_Do_UnknownMethod(t *testing.T) {
	sockPath, dial := startRealDaemonSocket(t)
	c := New(sockPath, dial, 10*time.Second)

	err := c.Do(context.Background(), "bogus.method", nil, nil)
	if err == nil {
		t.Fatal("Do: expected an error for an unregistered method")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("err = %v, want KindNotFound", err)
	}
}

// TestClient_Do_SocketNotFound proves dialing a socket nothing listens on
// surfaces KindUnavailable, not a raw net.OpError.
func TestClient_Do_SocketNotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-listens-here.sock")
	c := New(missing, func(ctx context.Context, p string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	}, 2*time.Second)

	err := c.Do(context.Background(), "status.get", nil, nil)
	if err == nil {
		t.Fatal("Do: expected an error dialing a socket nothing listens on")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestClient_Do_Timeout proves a request that exceeds the client's
// timeout surfaces KindTimeout. Uses a real socket whose handler blocks
// past the client's deadline, so this exercises the real HTTP round trip
// timing out, not a synthetic context.DeadlineExceeded.
func TestClient_Do_Timeout(t *testing.T) {
	dir, err := os.MkdirTemp("", "clienttimeout")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	registry := rpc.NewRegistry()
	registry.Register("slow.method", func(ctx context.Context, _ json.RawMessage) (any, error) {
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
		}
		return map[string]string{"ok": "true"}, nil
	})

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: rpc.NewHandler(registry), ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	c := New(sockPath, func(ctx context.Context, p string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	}, 200*time.Millisecond)

	err = c.Do(context.Background(), "slow.method", nil, nil)
	if err == nil {
		t.Fatal("Do: expected a timeout error")
	}
	if !cascade.HasKind(err, cascade.KindTimeout) {
		t.Errorf("err = %v, want KindTimeout", err)
	}
}

// TestClient_Do_MalformedResponse proves a peer on the socket that writes
// a non-JSON-RPC body (still valid HTTP, invalid application payload)
// surfaces KindInternal rather than panicking or hanging — the transport
// succeeded, only the decode failed.
func TestClient_Do_MalformedResponse(t *testing.T) {
	dir, err := os.MkdirTemp("", "clientmalformed")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "d.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(rpcPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("this is not a JSON-RPC envelope"))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	c := New(sockPath, func(ctx context.Context, p string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	}, 5*time.Second)

	err = c.Do(context.Background(), "status.get", nil, nil)
	if err == nil {
		t.Fatal("Do: expected an error for a malformed response body")
	}
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Errorf("err = %v, want KindInternal", err)
	}
}
