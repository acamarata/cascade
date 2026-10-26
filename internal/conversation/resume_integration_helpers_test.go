//go:build !windows && integration

package conversation

// Purpose: shared pieces of TestResume_RealSocket_FullFlow
//   (resume_integration_test.go): the vaulted secret it seeds, a REAL
//   vault-backed egress.Engine, the real daemon it starts, and a JSON-RPC
//   POST helper over the daemon's unix socket. Split out under Art.10.3's
//   300-line cap.
// SPORT: internal.conversation.resume/ADDED (integration test)
//   (P1-E22-W5-S47-T2).

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
)

// resumeVaultedName/resumeVaultedValue are the operator secret the test
// seeds into a real vault before composing a turn that carries it in the
// clear; the conversation-resume egress pass must strip it.
const (
	resumeVaultedName  = "RESUME_IT_API_KEY"
	resumeVaultedValue = "sk-resume-it-canary-0000000000000000"
)

// newResumeIntegrationEgressEngine builds a REAL egress.Engine over a real,
// on-disk secrets.Broker (scrub_test.go's newScrubTestCustody, a file vault
// -- no real keychain), seeded with resumeVaultedName=resumeVaultedValue,
// the same construction as cmd/cascade/chat_resume_wiring.go.
func newResumeIntegrationEgressEngine(t *testing.T, dir string) *egress.Engine {
	t.Helper()
	broker, err := secrets.NewBroker(newScrubTestCustody(t, dir), nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if _, err := broker.Set(context.Background(), resumeVaultedName, []byte(resumeVaultedValue), secrets.SetUpdate); err != nil {
		t.Fatalf("seed vault: %v", err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("NewEgressVault: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

// startResumeDaemon runs internal/daemon.Run (the production entry point)
// serving registry and the SSE mirror on a fresh unix socket, stops it at
// cleanup, and returns an HTTP client dialing that socket.
func startResumeDaemon(t *testing.T, registry *rpc.Registry, bus *events.Bus, clock runtime.Clock) *http.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "resumeit") // short path: unix socket length limit
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "conv.sock")
	known := func(kind events.EventKind) bool { return kind == turnAppendedKind }
	srv := daemon.NewRPCServer(registry, rpc.NewSSEHandler(bus, turnAppendedNamespace, known, clock))
	signals, ready, done := make(chan os.Signal, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- daemon.Run(context.Background(), daemon.RunOptions{
			Settings: daemon.Settings{SocketPath: socketPath, ShutdownGrace: 2 * time.Second},
			PIDPath:  filepath.Join(t.TempDir(), "daemon.pid"), Clock: clock, Signals: signals, Ready: ready, Server: srv,
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("daemon.Run exited before becoming ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("daemon.Run never became ready")
	}
	t.Cleanup(func() {
		signals <- os.Interrupt
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("daemon.Run did not return after a termination signal")
		}
	})
	return &http.Client{Transport: ownerMarked{&http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}}}
}

// ownerMarked stamps every request with the owner CLI marker, the way the
// real client does: with the access policy on, the daemon derives the
// transport-verified Caller (which chat.create_thread requires) from it.
type ownerMarked struct{ next http.RoundTripper }

func (o ownerMarked) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(principal.HeaderOwnerCLI, "owner-cli")
	return o.next.RoundTrip(r)
}

// postRPC POSTs one JSON-RPC call over the socket and decodes its result
// into out, failing the test on a transport error or an RPC error.
func postRPC(t *testing.T, client *http.Client, method string, params, out any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": method, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode %s: %v", method, err)
	}
	if envelope.Error != nil {
		t.Fatalf("%s error: %+v", method, envelope.Error)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		t.Fatalf("unmarshal %s result: %v", method, err)
	}
}
