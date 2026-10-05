//go:build !windows && integration

package daemon

// Purpose: TestStatusWidgetChangedSSEFrameOnSocket proves the wire contract
// the Swift client is built against over a REAL daemon on a unix socket: a
// status.widget_changed frame arrives on GET /events (the default stream,
// ?filter=status.widget_changed accepted), its base64 payload decodes to a
// WidgetSnapshot equal to the status.widget result at the same seq, and a
// browser-shaped request (an Origin header) is refused with HTTP 403. The
// frame's raw bytes are the fixture
// testdata/fixture_status_widget_changed_sse.txt (rewritten only under
// CASCADE_TESTKIT_UPDATE_GOLDEN=1, never in CI).
//
// Needs "net"/"net/http", so it lives in the `-tags=integration` lane with
// this package's other real-socket tests (internal/build's no-network gate).
// The daemon is built as cmd/cascade builds it: NewRPCServer over the real
// rpc.Registry with the production RegisterStatusWidgetHandler, and the real
// rpc.SSEMux on a real events.Bus, served by Run on t's short temp socket.
//
// SPORT: daemon.status_widget (tests, P1-WID-08).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// readFrame reads one SSE record (id, data, blank line) from r and returns
// its raw bytes.
func readFrame(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	got := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				got <- nil
				return
			}
			buf.Write(line)
			if bytes.Equal(line, []byte("\n")) && bytes.Contains(buf.Bytes(), []byte("data:")) {
				got <- buf.Bytes()
				return
			}
		}
	}()
	select {
	case b := <-got:
		if b == nil {
			t.Fatal("the SSE stream ended before a frame arrived")
		}
		return b
	case <-time.After(10 * time.Second):
		t.Fatal("no status.widget_changed frame within 10s")
		return nil
	}
}

// serveWidgetSocket serves h's registry and bus on a short temp unix socket
// through the real Run, as cmd/cascade builds it, and stops it on cleanup.
func serveWidgetSocket(t *testing.T, h *widgetHarness) string {
	t.Helper()
	socketPath := filepath.Join(shortTempDir(t), "d.sock")
	known := func(kind events.EventKind) bool { return KnownStatusWidgetEventKind(kind) }
	srv := NewRPCServer(h.reg, rpc.NewSSEMux(rpc.NewSSEHandler(h.bus, statusWidgetNamespace, known, runtime.NewSystemClock())))
	signals, ready, done := make(chan os.Signal, 1), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- Run(context.Background(), RunOptions{Settings: Settings{SocketPath: socketPath, ShutdownGrace: 2 * time.Second},
			PIDPath: filepath.Join(t.TempDir(), "daemon.pid"), Clock: runtime.NewSystemClock(), Signals: signals, Ready: ready, Server: srv})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("Run exited before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run never became ready")
	}
	t.Cleanup(func() {
		signals <- os.Interrupt
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after a termination signal")
		}
	})
	return socketPath
}

func TestStatusWidgetChangedSSEFrameOnSocket(t *testing.T) {
	h := newWidgetHarness(t)
	h.seed("alpha-avail", registry.LaneStateAvailable, time.Time{})
	h.seed("alpha-limit", registry.LaneStateExhausted, harnessNow.Add(2*time.Hour))
	h.seed("alpha-auth", registry.LaneStateAuthRequired, time.Time{})

	socketPath := serveWidgetSocket(t, h)
	client := unixHTTPClient(socketPath)

	// Fail closed first: a browser-shaped request never reaches the stream.
	req, _ := http.NewRequest(http.MethodGet, "http://unix"+rpc.EventsPath, nil)
	req.Header.Set("Origin", "http://browser.invalid")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /events with an Origin: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /events with an Origin header = %d, want 403", resp.StatusCode)
	}

	// Subscribe before the tick: a stream with no resume token tails from now.
	resp, err = client.Get("http://unix" + rpc.EventsPath + "?filter=" + string(StatusWidgetChangedKind))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events?filter=%s: %v (%v)", StatusWidgetChangedKind, resp, err)
	}
	defer func() { _ = resp.Body.Close() }()
	h.ticker.step(t)
	frame := readFrame(t, bufio.NewReader(resp.Body))

	assertFrameMatchesRPC(t, client, frame)
	writeOrCompareFrameFixture(t, frame)
}

// assertFrameMatchesRPC decodes the frame's payload and requires it to equal
// the status.widget result the same daemon gives at the same seq.
func assertFrameMatchesRPC(t *testing.T, client *http.Client, frame []byte) {
	t.Helper()
	var data string
	for _, line := range strings.Split(string(frame), "\n") {
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	var env struct {
		Kind    string `json:"kind"`
		Source  string `json:"source"`
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &env); err != nil || env.Kind != string(StatusWidgetChangedKind) || env.Source != "status.widget" {
		t.Fatalf("frame data %q: kind %q source %q err %v", data, env.Kind, env.Source, err)
	}
	raw, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	var fromFrame, fromRPC capacity.WidgetSnapshot
	if err := json.Unmarshal(raw, &fromFrame); err != nil {
		t.Fatalf("payload is not a WidgetSnapshot: %v", err)
	}
	resp, err := client.Post("http://unix"+rpc.RPCPath, "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"status.widget","id":1}`))
	if err != nil {
		t.Fatalf("POST status.widget: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var rpcEnv struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcEnv); err != nil || json.Unmarshal(rpcEnv.Result, &fromRPC) != nil {
		t.Fatalf("decode status.widget result: %v", err)
	}
	a, _ := json.Marshal(fromFrame)
	b, _ := json.Marshal(fromRPC)
	if fromFrame.Seq != 1 || !bytes.Equal(a, b) || len(fromFrame.Rows) != 3 {
		t.Fatalf("frame snapshot (seq %d) != status.widget result:\n frame %s\n rpc   %s", fromFrame.Seq, a, b)
	}
}

// writeOrCompareFrameFixture holds testdata/fixture_status_widget_changed_sse.txt
// to frame, or rewrites it under CASCADE_TESTKIT_UPDATE_GOLDEN=1 outside CI.
func writeOrCompareFrameFixture(t *testing.T, frame []byte) {
	t.Helper()
	path := filepath.Join("testdata", "fixture_status_widget_changed_sse.txt")
	if os.Getenv("CASCADE_TESTKIT_UPDATE_GOLDEN") == "1" && os.Getenv("CI") == "" {
		if err := os.WriteFile(path, frame, 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(want, frame) {
		t.Fatalf("frame differs from %s (read err %v); regenerate with CASCADE_TESTKIT_UPDATE_GOLDEN=1\n got: %q\nwant: %q", path, err, frame, want)
	}
}
