//go:build !windows && integration

package daemon

// Purpose: R-16.80 Ruling 3(a)'s required proof: `cascade run`'s real
//   client path (a real net/http client, the same precedent P1-E20-W5-
//   S43-T2's TestClientLocalEcho_RealSocket_EchoPrecedesResponse and this
//   package's own TestRun_RealSocket_RPCAndSSE_EndToEnd use) driven
//   against a REAL daemon.Run over a REAL unix socket, asserting
//   "conductor.execute" answers with something other than JSON-RPC's
//   -32601 method-not-found. TestConductorExecute_RealSocket_MutationProof
//   is Ruling 3(b): delete/restore the registration call and requires
//   BOTH outcomes to be captured by hand (see this ticket's journal for
//   the quoted red/green transcript) — a single automated test cannot
//   assert on its own deletion, so this file documents the exact
//   commands the journal's transcript comes from.
// SPORT: internal/daemon (ADD, R-16.80).

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestConductorExecute_RealSocket_NonMethodNotFound is Ruling 3(a): a real
// client, over a real socket, through the real Run entry point, gets a
// real answer to "conductor.execute" — never JSON-RPC's -32601. Before
// this ticket, registry.Registered(ConductorExecuteMethod) was always
// false in production and this exact POST would have failed with
// "method not found: conductor.execute".
func TestConductorExecute_RealSocket_NonMethodNotFound(t *testing.T) {
	dir := shortTempDir(t)
	socketPath := filepath.Join(dir, "run.sock")
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	clock := runtime.NewSystemClock()

	registry := rpc.NewRegistry()
	manifest := NewManifest(nil, clock)
	auditWriter := audit.New(storetest.NewMemStore(), clock, nil)
	if err := RegisterConductorExecuteHandler(registry, manifest, fakeRegistryReader{}, fakeQuotaSpiller{}, nil, auditWriter, clock); err != nil {
		t.Fatalf("RegisterConductorExecuteHandler: %v", err)
	}

	srv := NewRPCServer(registry, nil)
	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunOptions{
			Settings: Settings{SocketPath: socketPath, ShutdownGrace: 2 * time.Second},
			PIDPath:  pidPath,
			Clock:    clock,
			Signals:  signals,
			Ready:    ready,
			Server:   srv,
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}
	t.Cleanup(func() {
		signals <- os.Interrupt
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return after a termination signal")
		}
	})

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}}
	body := []byte(`{"jsonrpc":"2.0","method":"conductor.execute","id":"1","params":{"task_id":"t1","task_class":"chat","inputs":[{"role":"user","content":"hi"}]}}`)
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s over the real socket: %v", rpc.RPCPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode JSON-RPC response: %v", err)
	}
	if envelope.Error == nil {
		t.Fatal("conductor.execute: want a real construction-failure error (no ProviderResolver exists yet), got a nil error")
	}
	const codeMethodNotFound = -32601
	if envelope.Error.Code == codeMethodNotFound {
		t.Fatalf("conductor.execute returned method-not-found (-32601): %+v — the registration is not reaching the wire", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "conductor.execute: executor unavailable") {
		t.Errorf("conductor.execute error = %q, want it to name the real ErrConstructionFailed reason", envelope.Error.Message)
	}
}
