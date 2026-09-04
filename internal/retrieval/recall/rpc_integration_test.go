//go:build !windows && integration

package recall

// Purpose: the Art.2 external-contract proof for the recall.* namespace.
//   It serves the REAL internal/rpc Registry/Handler pipeline — the same
//   one cmd/cascade's composition root builds — over a REAL unix socket,
//   and calls it with real HTTP/1.1 POSTs from net/http. Nothing here
//   self-authors the JSON-RPC dialect: the request goes out over the wire
//   and the response comes back off it.
//
//   The scope assertion is repeated here, on the raw response BYTES,
//   because this is the last seam: everything below has been proven in
//   Go values, and a leak that only appears once an answer is serialized
//   would appear exactly here and nowhere earlier.
//
// Constraints: build-tagged "integration" because the no-network unit
//   lane (Art.7.2) forbids "net"/"net/http" in an untagged test file. The
//   Windows counterpart is the asserted refusal in
//   rpc_integration_windows_test.go.
// SPORT: internal.retrieval.recall.Handler/ADDED (P1-E06-W2-S11-T3).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// serveRecallRPC serves the real registry over a real unix socket and
// returns an HTTP client that reaches it.
func serveRecallRPC(t *testing.T, legs ...Leg) *http.Client {
	t.Helper()
	registry := rpc.NewRegistry()
	NewHandler(newTestService(t, legs...)).Register(registry)

	// A unix socket path is capped near 104 bytes on macOS and BSD, and
	// t.TempDir() embeds the test's own name, which pushes a long test
	// name past that cap. The socket therefore goes in a short MkdirTemp
	// directory; the CATALOG still lives under t.TempDir() (Art.7.1).
	sockDir, err := os.MkdirTemp("", "recallrpc")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "d.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{
		Handler:           rpc.NewHandler(registry),
		ConnContext:       rpc.ConnContext,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		},
	}}
}

// postRecall sends one JSON-RPC 2.0 request over the socket and returns
// the raw response body.
func postRecall(t *testing.T, c *http.Client, method string, params any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": method, "params": params,
		"client_version": "1.0.0",
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, "http://unix"+rpc.RPCPath, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d body %s", method, resp.StatusCode, raw)
	}
	return raw
}

// TestRecallRPCOverARealSocket drives recall.query and its v1-parity
// alias through a real JSON-RPC 2.0 exchange.
func TestRecallRPCOverARealSocket(t *testing.T) {
	c := serveRecallRPC(t)
	for _, method := range []string{MethodQuery, MethodSearchAlias} {
		t.Run(method, func(t *testing.T) {
			raw := postRecall(t, c, method, QueryParams{
				Query: testQuery, Scope: "project/cascade", Cite: true,
			})
			var envelope struct {
				JSONRPC string          `json:"jsonrpc"`
				Result  QueryResult     `json:"result"`
				Error   json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("decode response %s: %v", raw, err)
			}
			if envelope.JSONRPC != "2.0" {
				t.Errorf("response is not JSON-RPC 2.0: %s", raw)
			}
			if len(envelope.Error) != 0 {
				t.Fatalf("call failed: %s", envelope.Error)
			}
			if len(envelope.Result.Results) == 0 {
				t.Fatal("the socket returned no results")
			}
			if len(envelope.Result.Citations) == 0 {
				t.Error("the socket response carried no citations array")
			}
			if !strings.Contains(envelope.Result.Rendered, "handbook/fusion.md") {
				t.Errorf("--cite rendered nothing:\n%s", envelope.Result.Rendered)
			}
		})
	}
}

// TestRecallRPCScopeHoldsOverARealSocket is the composition assertion on
// the wire bytes themselves, with a leg that hands back a chunk it was
// never authorized to see.
func TestRecallRPCScopeHoldsOverARealSocket(t *testing.T) {
	c := serveRecallRPC(t, lexicalLeg{}, leakyLeg{chunkID: "c-secret"})
	raw := postRecall(t, c, MethodQuery, QueryParams{
		Query: testQuery, Scope: "project/cascade", Cite: true,
	})
	if !strings.Contains(string(raw), "handbook/fusion.md") {
		t.Fatalf("the response returned nothing authorized, so the assertion would be vacuous:\n%s", raw)
	}
	for _, forbidden := range []string{"quokka", "secrets.md", "journal", "c-secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the raw socket response carried %q:\n%s", forbidden, raw)
		}
	}
}

// TestRecallRPCRefusalOverARealSocket: a refusal crosses the wire as the
// taxonomy's own code, and carries nothing about the content it withheld.
func TestRecallRPCRefusalOverARealSocket(t *testing.T) {
	c := serveRecallRPC(t)
	raw := postRecall(t, c, MethodQuery, QueryParams{
		Query: testQuery, Scope: "project/cascade", Corpus: []string{"journal"},
	})
	var envelope struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	if envelope.Error.Code != cascade.RPCCodeNotFound {
		t.Fatalf("code = %d, want not-found; body %s", envelope.Error.Code, raw)
	}
	for _, forbidden := range []string{"quokka", "secrets.md", "c-secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("the refusal carried %q:\n%s", forbidden, raw)
		}
	}
}
