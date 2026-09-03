//go:build !windows && integration

package rpc

// Purpose: proves peerCredFromConn's real platform syscall path (SO_PEERCRED
//   on linux, LOCAL_PEERCRED on darwin) against an ACTUAL unix socket —
//   handler_test.go's spoofed-ConnContext tests prove Handler's logic but
//   never call the real platform code path. R-14.133 permits a build-tagged
//   test its own file since a `//go:build !windows` file cannot share a
//   file with the untagged unit tests a plain `go test` must also run.
// Constraints: Art.7.1 — the listener binds under t.TempDir(), never a
//   fixed path; this file is excluded from the no-network-unit-lane gate
//   by its build tag exactly as internal/daemon's own *_unix_test.go files
//   are.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortTempDir returns a temp directory NOT rooted under t.TempDir(): the
// latter embeds the full (potentially long) test name in its path, which a
// unix domain socket's sockaddr_un.sun_path (~104 bytes on Darwin) can
// overflow on its own before a single filename is even appended —
// observed directly by this file's own tests. Same helper and rationale as
// internal/daemon's shortTempDir (daemon_helpers_test.go); t.Cleanup
// removes it the same way t.TempDir()'s own cleanup would (Art.7.1: still
// a genuine OS temp path, never a fixed one).
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rpctd")
	if err != nil {
		t.Fatalf("shortTempDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestPeerCredFromConn_RealUnixSocket(t *testing.T) {
	sockPath := filepath.Join(shortTempDir(t), "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	var gotUID int
	var gotOK bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		gotUID, gotOK = peerCredFromConn(conn)
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()
	<-done

	if !gotOK {
		t.Fatal("peerCredFromConn: ok=false for a real unix socket connection")
	}
	if gotUID != os.Getuid() {
		t.Errorf("gotUID = %d, want %d (this process's own UID, since it dialed itself)", gotUID, os.Getuid())
	}
}

// TestHandler_RealSocketRoundTrip exercises the full stack — ConnContext
// resolving a REAL peer UID, then the owner check, then Parse/Dispatch —
// over an http.Server actually Serving a real unix listener, rather than
// httptest.NewRequest's synthetic context.
func TestHandler_RealSocketRoundTrip(t *testing.T) {
	sockPath := filepath.Join(shortTempDir(t), "test.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	withOwnerUID(t, os.Getuid())
	reg := NewRegistry()
	reg.Register("ping", func(_ context.Context, _ json.RawMessage) (any, error) {
		return "pong", nil
	})

	srv := &http.Server{Handler: NewHandler(reg), ConnContext: ConnContext}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sockPath)
		},
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Post("http://unix"+RPCPath, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","method":"ping","id":1}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var env ResponseEnvelope
	if decErr := json.NewDecoder(resp.Body).Decode(&env); decErr != nil {
		t.Fatalf("decode: %v", decErr)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	if env.Result != "pong" {
		t.Errorf("Result = %v, want pong", env.Result)
	}
}

// fakeConn is a net.Conn that is deliberately NOT a *net.UnixConn, so
// ConnContext must fail closed on it. It lives in the integration-tagged
// file because naming net.Conn at all requires importing net, which the
// no-network-unit-lane gate forbids in the default lane — the gate is
// import-based, and moving the test is the honest response to it rather
// than contriving a stand-in type that does not actually satisfy net.Conn.
type fakeConn struct{ net.Conn }

func TestConnContext_NonUnixConnFailsClosed(t *testing.T) {
	ctx := ConnContext(context.Background(), fakeConn{})
	cred, ok := ctx.Value(peerCredKey{}).(peerCred)
	if !ok {
		t.Fatal("ConnContext must always store a peerCred value")
	}
	if cred.ok {
		t.Error("a non-net.Conn connection type must resolve ok=false (fail-closed)")
	}
}
