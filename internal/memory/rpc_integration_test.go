//go:build !windows && integration

package memory

// Purpose: the Art.2 external-contract proof for the memory.* namespace.
//   It serves the REAL internal/rpc Registry/Handler pipeline — the same
//   one cmd/cascade's composition root builds — over a REAL unix socket,
//   and calls it with real HTTP/1.1 POSTs from net/http. Nothing here
//   self-authors the JSON-RPC dialect: the requests go out over the wire
//   and the responses come back off it, and the golden fixture is a
//   recording of that exchange, not a hand-written guess at it.
// Constraints: build-tagged "integration" because the no-network unit
//   lane (Art.7.2) forbids "net"/"net/http" in an untagged test file. The
//   Windows counterpart is the asserted refusal in
//   rpc_integration_windows_test.go.
// SPORT: internal.memory.rpc.Handler (ADD, P1-E07-W2-S13-T3).

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
)

// goldenUpdateEnvVar regenerates internal/memory/testdata/rpc-golden.json
// from a live run. The fixture is a RECORDING; this is how it is
// re-recorded when the wire shape legitimately changes.
const goldenUpdateEnvVar = "CASCADE_MEMORY_GOLDEN_UPDATE"

// serveMemoryRPC serves the real registry over a real unix socket and
// returns an HTTP client that reaches it.
func serveMemoryRPC(t *testing.T) *http.Client {
	t.Helper()
	base := t.TempDir()
	// A frozen clock, so the recorded fixture is a function of the
	// request and nothing else. A wall clock here would make the golden
	// unmatchable one second after it was captured.
	clk := newTestClock()
	registry := rpc.NewRegistry()
	NewHandler(NewFileStore(base, clk), clk).Register(registry)

	// A unix socket path is capped near 104 bytes on macOS and BSD, and
	// t.TempDir() embeds the test's own name, which pushes a long test
	// name past that cap and fails with a bare "invalid argument". The
	// socket therefore goes in a short MkdirTemp directory, the same
	// workaround internal/client's own socket test records; the STORE
	// still lives in t.TempDir() (Art.7.1).
	sockDir, err := os.MkdirTemp("", "memrpc")
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

// post sends one JSON-RPC 2.0 request over the socket and returns the raw
// response body.
func post(t *testing.T, c *http.Client, id, method string, params any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
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

// normalize renders one response body deterministically, replacing the
// build-dependent server_version so the fixture pins the SHAPE of the
// exchange rather than the version of the binary that recorded it.
func normalize(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, raw)
	}
	if _, ok := obj["server_version"]; ok {
		obj["server_version"] = "<server_version>"
	}
	out, err := json.MarshalIndent(obj, "  ", "  ")
	if err != nil {
		t.Fatalf("re-marshal response: %v", err)
	}
	return out
}

// TestRPCGoldenRoundTrip records (or asserts) one real
// remember → recall exchange over the socket.
func TestRPCGoldenRoundTrip(t *testing.T) {
	c := serveMemoryRPC(t)
	remember := post(t, c, "1", MethodRemember, RememberParams{
		Content: "the golden note", Type: "project", Name: "golden-note", Provenance: "session-golden",
	})
	recall := post(t, c, "2", MethodRecall, RecallParams{Query: "golden", K: 5})

	got := map[string]json.RawMessage{
		"remember": normalize(t, remember),
		"recall":   normalize(t, recall),
	}
	rendered, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("render fixture: %v", err)
	}
	rendered = append(rendered, '\n')

	path := filepath.Join("testdata", "rpc-golden.json")
	if os.Getenv(goldenUpdateEnvVar) != "" {
		if err := os.WriteFile(path, rendered, 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("wrote %s from a live daemon run", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture (set %s=1 to record it): %v", goldenUpdateEnvVar, err)
	}
	if string(rendered) != string(want) {
		t.Errorf("live exchange does not match the recorded fixture\n--- got ---\n%s\n--- want ---\n%s", rendered, want)
	}
}

// TestRPCOverSocketCoversEveryMethod drives all four methods over the real
// wire, including the destructive one, and proves the tombstone is visible
// through the socket rather than only through the store.
func TestRPCOverSocketCoversEveryMethod(t *testing.T) {
	c := serveMemoryRPC(t)
	for _, name := range []string{"one", "two"} {
		post(t, c, "r-"+name, MethodRemember, RememberParams{Content: "body " + name, Name: name})
	}
	var listed struct {
		Result ListResult `json:"result"`
	}
	decode(t, post(t, c, "l1", MethodList, ListParams{}), &listed)
	if got := addressesOf(listed.Result.Units); strings.Join(got, ",") != "project/one,project/two" {
		t.Fatalf("list over the socket = %v", got)
	}

	var forgotten struct {
		Result ForgetResult `json:"result"`
	}
	decode(t, post(t, c, "f1", MethodForget, ForgetParams{ID: "project/one"}), &forgotten)
	if !forgotten.Result.Forgotten {
		t.Fatalf("forget over the socket returned %+v", forgotten.Result)
	}

	decode(t, post(t, c, "l2", MethodList, ListParams{}), &listed)
	if got := addressesOf(listed.Result.Units); strings.Join(got, ",") != "project/two" {
		t.Fatalf("after forget, list = %v, want only project/two", got)
	}
}

// TestRPCOverSocketErrorsAreTyped proves a refusal crosses the wire as a
// JSON-RPC error object carrying the taxonomy kind, not as a 500.
func TestRPCOverSocketErrorsAreTyped(t *testing.T) {
	c := serveMemoryRPC(t)
	cases := []struct {
		name   string
		method string
		params any
	}{
		{"unknown kind", MethodRemember, RememberParams{Content: "x", Type: "bogus"}},
		{"absent address", MethodForget, ForgetParams{ID: "project/absent"}},
		{"malformed address", MethodForget, ForgetParams{ID: "not-an-address"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var resp struct {
				Error *rpc.ErrorObject `json:"error"`
			}
			decode(t, post(t, c, "e1", tc.method, tc.params), &resp)
			if resp.Error == nil {
				t.Fatal("expected a JSON-RPC error object")
			}
			if resp.Error.Code >= 0 {
				t.Errorf("error code = %d, want a negative JSON-RPC code", resp.Error.Code)
			}
		})
	}
}

// decode unmarshals a response body into dst, failing the test on garbage.
func decode(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
}
