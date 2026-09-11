//go:build integration

// Purpose: TestStreamRealSSE, the Art.2 real-counterpart proof for
//
//	P1-E11-W3-S23-T3: a real *events.Bus (D/S-06.T4) bridging real
//	internal/rpc.SSEHandler GET /events connections over a real unix
//	socket, with job.cancel dispatched through a real
//	internal/rpc.Registry/Handler POST /rpc exchange - never a
//	self-authored SSE or JSON-RPC dialect. Build-tagged "integration"
//	because it imports "net"/"net/http" and opens a real socket
//	(internal/build's no-network-unit-lane gate, Art.7.2); see
//	stream_test.go/cancel_test.go for this ticket's network-free unit
//	coverage of the same behavior through fakes.
//
// SPORT: conductor.streaming/ADD (P1-E11-W3-S23-T3), integration proof.
package conductor

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// startRealSSEDaemon builds the real events.Bus + SSEHandler + Registry
// pipeline (mirroring internal/client/client_integration_test.go's own
// startRealDaemonSocket pattern) with job.cancel registered against exec,
// serves it over a real unix socket, and returns an *http.Client dialing
// that socket plus a teardown func.
func startRealSSEDaemon(t *testing.T, exec *Executor) (client *http.Client, teardown func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "conductorit")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	socketPath := filepath.Join(dir, "d.sock")

	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	bus := events.New(storetest.NewMemStore(), clock)
	exec.SetEventBridge(bus)

	known := func(k events.EventKind) bool { return strings.HasPrefix(string(k), "job:") }
	sse := rpc.NewSSEHandler(bus, conductorEventNamespace, known, clock)

	registry := rpc.NewRegistry()
	RegisterHandlers(registry, exec)
	handler := rpc.NewHandlerWithSSE(registry, sse)

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	srv := &http.Server{Handler: handler, ConnContext: rpc.ConnContext}
	go func() { _ = srv.Serve(ln) }()

	client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}}
	teardown = func() {
		_ = srv.Close()
		_ = bus.Close()
		_ = os.RemoveAll(dir)
	}
	return client, teardown
}

// sseReader owns the ONE goroutine that scans a response body for
// real, dechunked SSE "data:" lines and feeds them to a channel other
// goroutines drain from. A response body may only ever be read by a
// single goroutine at a time (io.Reader has no such guarantee otherwise),
// so every call site in this test shares one sseReader per connection
// rather than starting a fresh scanner per readSSELines call.
type sseReader struct {
	lines chan string
}

func newSSEReader(body io.Reader) *sseReader {
	r := &sseReader{lines: make(chan string, 8)}
	go func() {
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				r.lines <- strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		close(r.lines)
	}()
	return r
}

// next reads n more data lines, bounded by waitTimeout - never a bare
// blocking read.
func (r *sseReader) next(t *testing.T, n int) []string {
	t.Helper()
	var got []string
	for i := 0; i < n; i++ {
		select {
		case l, ok := <-r.lines:
			if !ok {
				t.Fatalf("SSE stream closed after %d/%d expected data lines", len(got), n)
			}
			got = append(got, l)
		case <-time.After(waitTimeout):
			t.Fatalf("read only %d/%d SSE data lines before timeout", len(got), n)
		}
	}
	return got
}

// decodeSSEPayload decodes one raw SSE data line as internal/rpc/sse.go's
// own envelope ({"seq":...,"kind":...,"source":...,"payload":"<base64>"})
// and returns the base64-decoded payload bytes - the real wire shape this
// ticket's client side must also decode, exercised here rather than
// hand-parsed with a substring check.
func decodeSSEPayload(t *testing.T, line string) []byte {
	t.Helper()
	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &envelope); err != nil {
		t.Fatalf("decode SSE envelope %q: %v", line, err)
	}
	raw, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatalf("base64-decode SSE payload %q: %v", envelope.Payload, err)
	}
	return raw
}

func TestStreamRealSSE(t *testing.T) {
	exec, deps := newReadyExecutor(t)
	// proceed gates the fake provider's delta send until AFTER the test's
	// GET /events call has returned - internal/rpc/sse.go's own doc
	// comment names its no-resume-token behavior as "tail from now, not
	// history": an event published before the subscription opens is
	// never delivered to it. Subscribing before publishing (not a sleep,
	// a real synchronization point: client.Get does not return until the
	// server has written its SSE prelude, which happens strictly after
	// Subscribe) is what makes this ordering deterministic rather than a
	// race.
	proceed := make(chan struct{})
	deps.prov.streamFn = func(ctx context.Context, _ provider.ChatRequest, sink provider.StreamSink) error {
		select {
		case <-proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
		if err := sink(provider.StreamEvent{Kind: provider.StreamEventDelta, Delta: "hi"}); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}

	client, teardown := startRealSSEDaemon(t, exec)
	defer teardown()

	id, ch, cancelFn, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("ExecuteStreamJob: %v", err)
	}

	resp, err := client.Get("http://unix/events?filter=job:" + string(id))
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events status = %d, want 200", resp.StatusCode)
	}

	// Second, concurrent execution: its events must never bleed into the
	// first subscription (filtered by exact job_id kind match).
	other, otherCh, otherCancel, err := exec.ExecuteStreamJob(context.Background(), validReq())
	if err != nil {
		t.Fatalf("second ExecuteStreamJob: %v", err)
	}
	if other == id {
		t.Fatal("two ExecuteStreamJob calls minted the same job id")
	}

	close(proceed) // both fake provider goroutines may now send their delta

	sse := newSSEReader(resp.Body)
	lines := sse.next(t, 1)
	if !strings.Contains(string(decodeSSEPayload(t, lines[0])), `"delta"`) {
		t.Fatalf("first SSE payload = %q, want a delta-kind payload", decodeSSEPayload(t, lines[0]))
	}
	if !strings.Contains(lines[0], "job:"+string(id)) {
		t.Fatalf("first SSE line = %q, want it tagged with job:%s", lines[0], id)
	}
	if strings.Contains(lines[0], string(other)) {
		t.Fatalf("subscription for job %s observed the other job's id %s in its envelope - event bleed", id, other)
	}

	cancelFn()
	drainStream(t, ch)
	lines = sse.next(t, 1)
	if !strings.Contains(string(decodeSSEPayload(t, lines[0])), `"cancelled"`) {
		t.Fatalf("second SSE payload = %q, want the cancelled terminal event", decodeSSEPayload(t, lines[0]))
	}

	// job.cancel over a real POST /rpc exchange, on the already-terminal
	// job: idempotent success, no error.
	rpcBody := strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"job.cancel","params":{"job_id":"` + string(id) + `"}}`)
	rpcResp, err := client.Post("http://unix/rpc", "application/json", rpcBody)
	if err != nil {
		t.Fatalf("POST /rpc job.cancel: %v", err)
	}
	defer func() { _ = rpcResp.Body.Close() }()
	if rpcResp.StatusCode != http.StatusOK {
		t.Fatalf("job.cancel over the wire returned status %d, want 200", rpcResp.StatusCode)
	}

	otherCancel()
	drainStream(t, otherCh)
}
