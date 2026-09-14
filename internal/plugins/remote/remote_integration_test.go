//go:build integration

package remote

// Purpose: the real-socket counterpart to remote_test.go/remote_edge_test.go's
// fakeDoer-based unit coverage: proves realDoer (doer.go) and the whole
// dialRemote pipeline actually work over a REAL loopback HTTP server —
// moved behind the integration build tag because internal/build's
// TestNoNetworkUnitTest_RealTreeGreen gate forbids "net"/"net/http" in
// any non-integration _test.go, tree-wide (AGENT-BRIEF/LANE-RULES §6).
// Per AGENT-BRIEF, an integration-tagged test contributes NOTHING to
// measured coverage — this file exists for end-to-end confidence in
// realDoer specifically, the one file the fakeDoer-based unit tests
// never exercise (mirrors internal/plugins/registryfetch's own doer.go/
// doer_integration_test.go split for the identical reason).
// SPORT: internal/plugins/remote (ADD) — P1-E15-W4-S33-T4.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

func handshakeServer(t *testing.T, respond func(handshakeRequest) handshakeResponse) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != handshakePath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req handshakeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := respond(req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func serverHostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split %q: %v", srv.URL, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

func TestRemoteRuntime_RealDoer_HandshakeSuccess_Integration(t *testing.T) {
	srv := handshakeServer(t, func(req handshakeRequest) handshakeResponse {
		return handshakeResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Result: &handshakeResult{ABIVersion: HostABIVersionV1}}
	})
	host, port := serverHostPort(t, srv)
	cfg := RemoteRuntimeConfig{Host: host, Port: port, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}

	conn, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, nil)
	if err != nil {
		t.Fatalf("dialRemote over a real loopback server: %v", err)
	}
	if conn.abiVersion != HostABIVersionV1 {
		t.Errorf("conn.abiVersion = %d, want %d", conn.abiVersion, HostABIVersionV1)
	}
}

func TestRemoteRuntime_RealDoer_ConnectionRefused_Integration(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	cfg := RemoteRuntimeConfig{Host: host, Port: port, ABIVersion: HostABIVersionV1, Timeout: 2 * time.Second}
	_, err = dialRemote(context.Background(), cfg, &passthroughInterceptor{}, nil)
	if err == nil {
		t.Fatal("dialRemote over a closed port: want a connection-refused error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnavailable {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnavailable, true)", kind, ok)
	}
}

func TestRemoteRuntime_RealDoer_Timeout_Integration(t *testing.T) {
	srv := handshakeServer(t, func(req handshakeRequest) handshakeResponse {
		time.Sleep(150 * time.Millisecond)
		return handshakeResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Result: &handshakeResult{ABIVersion: HostABIVersionV1}}
	})
	host, port := serverHostPort(t, srv)
	cfg := RemoteRuntimeConfig{Host: host, Port: port, ABIVersion: HostABIVersionV1, Timeout: 20 * time.Millisecond}

	_, err := dialRemote(context.Background(), cfg, &passthroughInterceptor{}, nil)
	if err == nil {
		t.Fatal("dialRemote over a slow real server: want a timeout error, got nil")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindTimeout {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindTimeout, true)", kind, ok)
	}
}
