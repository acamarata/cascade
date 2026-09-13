//go:build !windows && integration

package rpc

// Purpose: TestSupervisorRPCRoundTrip — proves supervisor.snapshot over a
// REAL unix domain socket (net.Listen("unix", ...) + a real *http.Server
// Serving it), mirroring TestHandler_RealSocketRoundTrip
// (handler_unix_integration_test.go) exactly: this is the real-socket half
// Art.2 asks for, which testdata/README.md's supervisor-rpc-fixture.json
// note documents as scoped out of the Registry.Dispatch-only fixture.
// R-14.133: a build-tagged test needs its own file, since a
// "//go:build !windows" file cannot share a file with the untagged unit
// tests a plain `go test` must also run.

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

func TestSupervisorRPCRoundTrip(t *testing.T) {
	sockPath := filepath.Join(shortTempDir(t), "supervisor.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	withOwnerUID(t, os.Getuid())
	reg := NewRegistry()
	RegisterSupervisorHandlers(reg, &fakeSupervisorSource{result: populatedSnapshot()})

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
		strings.NewReader(`{"jsonrpc":"2.0","method":"supervisor.snapshot","id":1}`))
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

	resultBytes, err := json.Marshal(env.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var snap SupervisorSnapshotResult
	if err := json.Unmarshal(resultBytes, &snap); err != nil {
		t.Fatalf("unmarshal SupervisorSnapshotResult: %v", err)
	}
	if snap.SchemaVersion != SupervisorSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", snap.SchemaVersion, SupervisorSchemaVersion)
	}
	if snap.AttentionQueueDepth == 0 || len(snap.Sessions) == 0 {
		t.Errorf("snapshot came back with zero/empty fields over the real socket: %+v", snap)
	}
}
