//go:build !windows && integration

package conversation

// Purpose: TestAcceptanceLiveMirror -- S-44.T5 AC1, both directions.
//   Direction 1 extends sse_integration_test.go's real-daemon SSE harness
//   (S-43.T2's Art.2 proof) as this ticket's own named check target.
//   Direction 2 drives the same real unix-socket JSON-RPC transport
//   cascadepa_wiring.go's cascadePAClient.OneShot uses, identical wire
//   shape.
//
// CONTRACT DEVIATION: cascadePAClient/newCascadePAClient
// (internal/plugins/cascadepa_wiring.go) are unexported, so this test
// calls internal/client.New with client.UnixDialer (the same transport
// cascadePAClient.rpcClient() builds) and encodes appendTurnParams by
// hand -- the real transport/wire contract, only the thin wrapper is
// bypassed. `cascade chat` was rejected: every invocation returns
// errReplyGenerationUnavailable (no reply generator yet -- S-89.T6), an
// untestable dependency this proof does not need.
//
// PLATFORM: "!windows" matches sse_integration_test.go. ModeEmbedded
// (Windows tier-2) has no daemon/socket/SSE to mirror on at all.
// SPORT: internal.conversation/acceptance (ADDED, tests-only) (P1-E20-W5-S44-T5).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// acceptanceShortTempDir mirrors sse_integration_test.go's shortTempDirIT:
// sockaddr_un.sun_path is short enough (~104 bytes on darwin) that
// t.TempDir()'s long, test-name-embedding path can overflow it.
func acceptanceShortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "accim")
	if err != nil {
		t.Fatalf("acceptanceShortTempDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func acceptanceUnixHTTPClient(socketPath string) *http.Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	return &http.Client{Transport: &http.Transport{DialContext: dial}}
}

// acceptanceLiveMirrorHarness starts a REAL daemon.Run instance over a
// REAL unix socket, real Adapter handlers, and a real SSE handler --
// generalized from sse_integration_test.go for both directions.
type acceptanceLiveMirrorHarness struct {
	socketPath string
	httpClient *http.Client
	done       <-chan error
}

// newAcceptanceLiveMirrorHarness wires wrapBus (if non-nil) between the
// real *events.Bus and the Adapter, so a test can gate the Adapter's own
// Publish calls while the SSE handler stays on the unwrapped real bus.
func newAcceptanceLiveMirrorHarness(t *testing.T, wrapBus func(EventBus) EventBus) acceptanceLiveMirrorHarness {
	t.Helper()
	dir := acceptanceShortTempDir(t)
	socketPath := filepath.Join(dir, "conv.sock")
	pidPath := filepath.Join(t.TempDir(), "daemon.pid")
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	store := newTestStore(t)
	var adapterBus EventBus = bus
	if wrapBus != nil {
		adapterBus = wrapBus(bus)
	}
	adapter := NewAdapter(store, adapterBus, passthroughSubst{}, clock, "")
	registry := rpc.NewRegistry()
	adapter.RegisterHandlers(registry)
	known := func(kind events.EventKind) bool { return kind == turnAppendedKind }
	srv := daemon.NewRPCServer(registry, rpc.NewSSEHandler(bus, turnAppendedNamespace, known, clock))

	signals := make(chan os.Signal, 1)
	ready := make(chan struct{})
	done := make(chan error, 1)
	opts := daemon.RunOptions{
		Settings: daemon.Settings{SocketPath: socketPath, ShutdownGrace: 2 * time.Second},
		PIDPath:  pidPath, Clock: clock, Signals: signals, Ready: ready, Server: srv,
	}
	go func() { done <- daemon.Run(context.Background(), opts) }()
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
	return acceptanceLiveMirrorHarness{socketPath: socketPath, httpClient: acceptanceUnixHTTPClient(socketPath), done: done}
}

// firstCallGatedEventBus wraps a real EventBus and, for exactly its FIRST
// Publish call, blocks the caller until release is closed -- signaling
// entered first so a test can observe the handler synchronously blocked
// inside Publish (TestAcceptanceLiveMirror's ordering proof, below).
// Later calls (direction 2's own append) pass straight through.
type firstCallGatedEventBus struct {
	real    EventBus
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (g *firstCallGatedEventBus) Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error) {
	g.once.Do(func() { close(g.entered); <-g.release })
	return g.real.Publish(ctx, namespace, kind, source, payload)
}

// acceptanceClientAppendParams/Segment mirror cascadepa_wiring.go's
// unexported appendTurnParams/appendSegmentWire (header CONTRACT DEVIATION).
type acceptanceClientAppendParams struct {
	ThreadID    string                          `json:"thread_id"`
	Role        string                          `json:"role"`
	Segments    []acceptanceClientAppendSegment `json:"segments"`
	PrivacyMode string                          `json:"privacy_mode,omitempty"`
}

type acceptanceClientAppendSegment struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// acceptancePostResult carries a Post outcome across a goroutine.
type acceptancePostResult struct {
	resp *http.Response
	err  error
}

// TestAcceptanceLiveMirror is the ticket's named check target; both
// directions share one real daemon instance.
func TestAcceptanceLiveMirror(t *testing.T) {
	gate := &firstCallGatedEventBus{entered: make(chan struct{}), release: make(chan struct{})}
	h := newAcceptanceLiveMirrorHarness(t, func(bus EventBus) EventBus { gate.real = bus; return gate })

	t.Run("core_rpc_append_arrives_on_the_sse_stream_before_the_rpc_response", func(t *testing.T) {
		sseResp, err := h.httpClient.Get("http://unix" + rpc.EventsPath)
		if err != nil {
			t.Fatalf("GET %s over the real socket: %v", rpc.EventsPath, err)
		}
		t.Cleanup(func() { _ = sseResp.Body.Close() })
		reader := bufio.NewReader(sseResp.Body)
		// echoLine carries the raw "data:" line for decoding below.
		echoLine := make(chan string, 1)
		go func() {
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				line, rerr := reader.ReadString('\n')
				if rerr != nil {
					return
				}
				if strings.HasPrefix(line, "data:") {
					echoLine <- line
					return
				}
			}
		}()
		// POST runs in its own goroutine: Publish is about to be gated
		// below, so a synchronous handler must not block this goroutine.
		body := []byte(`{"jsonrpc":"2.0","method":"chat.append_turn","id":"lm1","params":{"thread_id":"th-lm-dir1","role":"user","segments":[{"kind":"text","content":"direction one"}]}}`)
		postDone := make(chan acceptancePostResult, 1)
		go func() {
			resp, perr := h.httpClient.Post("http://unix"+rpc.RPCPath, "application/json", bytes.NewReader(body))
			postDone <- acceptancePostResult{resp: resp, err: perr}
		}()
		// CLIENT-LOCAL ECHO ordering: not a "whichever fires first" race
		// (a goroutine reaching Publish can beat a socket round trip
		// regardless of correctness). Hold Publish blocked for a bounded
		// window and confirm no response arrives: a synchronous handler
		// cannot respond while blocked inside this call (never a false
		// failure); a fire-and-forget mutation decouples the response
		// from the block, so it arrives well inside the window instead.
		select {
		case <-gate.entered:
		case r := <-postDone:
			t.Fatalf("CLIENT-LOCAL ECHO ordering violated: RPC response (err=%v) arrived before the handler entered emitTurnAppended's Publish call -- the emit is not synchronous", r.err)
		case <-time.After(5 * time.Second):
			t.Fatal("emitTurnAppended's Publish was never entered within 5s -- the SSE mirror handler may not be wired")
		}
		select {
		case r := <-postDone:
			t.Fatalf("CLIENT-LOCAL ECHO ordering violated: RPC response (err=%v) arrived while Publish was still deliberately blocked -- the emit raced ahead of the response", r.err)
		case <-time.After(300 * time.Millisecond):
		}
		close(gate.release) // let Publish, the handler, and the response proceed
		var pr acceptancePostResult
		select {
		case pr = <-postDone:
		case <-time.After(10 * time.Second):
			t.Fatal("chat.append_turn's RPC response never arrived after releasing the gated Publish call")
		}
		if pr.err != nil {
			t.Fatalf("POST %s over the real socket: %v", rpc.RPCPath, pr.err)
		}
		resp := pr.resp
		t.Cleanup(func() { _ = resp.Body.Close() })
		// Decode the actual SSE "data:" wire line; check ThreadID/content.
		var rawLine string
		select {
		case rawLine = <-echoLine:
		case <-time.After(10 * time.Second):
			t.Fatal("SSE echo for the core-RPC-appended turn never arrived within 10s")
		}
		var sseEnvelope struct {
			Payload string `json:"payload"`
		}
		trimmed := strings.TrimSpace(strings.TrimPrefix(rawLine, "data:"))
		if err := json.Unmarshal([]byte(trimmed), &sseEnvelope); err != nil {
			t.Fatalf("decode SSE data: envelope: %v (line=%q)", err, rawLine)
		}
		ssePayloadBytes, err := base64.StdEncoding.DecodeString(sseEnvelope.Payload)
		if err != nil {
			t.Fatalf("base64-decode SSE envelope payload: %v", err)
		}
		var ssePayload turnAppendedPayload
		if err := json.Unmarshal(ssePayloadBytes, &ssePayload); err != nil {
			t.Fatalf("decode SSE turnAppendedPayload: %v", err)
		}
		if ssePayload.Turn.ThreadID != "th-lm-dir1" {
			t.Fatalf("SSE payload thread id = %q, want %q", ssePayload.Turn.ThreadID, "th-lm-dir1")
		}
		if len(ssePayload.Segments) != 1 || ssePayload.Segments[0].Content != "direction one" {
			t.Fatalf("SSE payload segments = %+v, want exactly one segment with content %q", ssePayload.Segments, "direction one")
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  any             `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatalf("decode JSON-RPC response: %v", err)
		}
		if envelope.Error != nil {
			t.Fatalf("chat.append_turn JSON-RPC error: %+v", envelope.Error)
		}
	})

	t.Run("cascade_pa_surface_append_appears_at_the_core_rpc_poll_path", func(t *testing.T) {
		// internal/client, the same transport cascadePAClient.rpcClient() builds.
		c := client.New(h.socketPath, client.UnixDialer, 5*time.Second)
		ctx := context.Background()
		var appendResult appendTurnResult
		params := acceptanceClientAppendParams{
			ThreadID: "th-lm-dir2", Role: "user",
			Segments: []acceptanceClientAppendSegment{{Kind: "text", Content: "direction two, via the cascade-pa transport"}},
		}
		if err := c.Do(ctx, MethodAppendTurn, params, &appendResult); err != nil {
			t.Fatalf("cascade-pa-surface append (real unix-socket client, conversation.MethodAppendTurn): %v", err)
		}
		if appendResult.ThreadID != "th-lm-dir2" || appendResult.TurnID == "" {
			t.Fatalf("append result = %+v, want a non-empty thread/turn id", appendResult)
		}
		// chat.get_thread over the same real socket/client -- the poll path.
		var polled getThreadResult
		if err := c.Do(ctx, MethodGetThread, getThreadParams{ThreadID: "th-lm-dir2"}, &polled); err != nil {
			t.Fatalf("chat.get_thread poll: %v", err)
		}
		if len(polled.Turns) != 1 {
			t.Fatalf("chat.get_thread poll returned %d turns, want 1: the cascade-pa-surface append did not appear at the core RPC poll path", len(polled.Turns))
		}
		got := polled.Turns[0]
		if got.Turn.ID != appendResult.TurnID {
			t.Fatalf("polled turn id = %q, want %q (the id the cascade-pa-surface append reported)", got.Turn.ID, appendResult.TurnID)
		}
		if len(got.Segments) != 1 || got.Segments[0].Content != "direction two, via the cascade-pa transport" {
			t.Fatalf("polled segments = %+v, want the exact content the cascade-pa-surface append sent", got.Segments)
		}
	})
}
