package rpc

// Purpose: the fleet.topology topic tests (P1-TOP-11), all through the
//   real SSEMux and the real shared SSEHandler over a real events.Bus.
// Constraints: only net/http/httptest, never net/http (hygiene gate), so
//   status codes are literal ints.

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/principal"
)

const fleetTopologyFixture = "fleet_topology_sse.txt"

// topologyMux mounts the real fleet.topology handler next to a default
// stream and an unrelated topic on one real bus.
func topologyMux(t *testing.T) (*SSEMux, *events.Bus) {
	t.Helper()
	bus, clock := newTestBus()
	t.Cleanup(func() { _ = bus.Close() })
	def := NewSSEHandler(bus, "daemon", KnownSupervisorEventKind, clock)
	other := NewSSEHandler(bus, "other-ns", func(k events.EventKind) bool { return k == "other-kind" }, clock)
	mux := NewSSEMux(def).
		WithTopic(FleetTopologyTopic, NewFleetTopologySSEHandler(bus, clock)).
		WithTopic("fleet.other", other)
	return mux, bus
}

// runMux serves mux in the background as the owner peer.
func runMux(ctx context.Context, mux *SSEMux, query string) (*syncRecorder, <-chan struct{}) {
	w := newSyncRecorder()
	base := context.WithValue(ctx, peerCredKey{}, peerCred{uid: 501, ok: true})
	req := httptest.NewRequest("GET", EventsPath+query, nil).WithContext(base)
	req.Host = "unix"
	done := make(chan struct{})
	go func() {
		mux.ServeHTTP(w, req)
		close(done)
	}()
	return w, done
}

func waitWrote(t *testing.T, w *syncRecorder) {
	t.Helper()
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })
}

func emitTopology(bus *events.Bus) {
	e := NewFleetTopologyEmitter(bus)
	e.EmitLaneHealthChanged(context.Background(), LaneHealthChangedPayload{
		LaneID: "lane-local-1", From: "healthy", To: "degraded", Reason: "probe-timeout",
	})
	e.EmitReconciled(context.Background(), FleetTopologyReconciledPayload{Generation: 7, Lanes: 3, Changed: 1})
}

// TestLaneHealthChangedSSE: the filtered fleet.topology subscriber gets
// lane.health_changed; another filter or another topic gets nothing.
func TestLaneHealthChangedSSE(t *testing.T) {
	withOwnerUID(t, 501)
	mux, bus := topologyMux(t)
	ctx, cancel := context.WithCancel(context.Background())
	want, wantDone := runMux(ctx, mux, "?topic=fleet.topology&filter=lane.health_changed")
	recon, reconDone := runMux(ctx, mux, "?topic=fleet.topology&filter=fleet.topology.reconciled")
	other, otherDone := runMux(ctx, mux, "?topic=fleet.other")
	def, defDone := runMux(ctx, mux, "")
	for _, w := range []*syncRecorder{want, recon, other, def} {
		waitWrote(t, w)
	}
	NewFleetTopologyEmitter(bus).EmitLaneHealthChanged(context.Background(),
		LaneHealthChangedPayload{LaneID: "lane-a", From: "healthy", To: "down"})
	waitFor(t, func() bool { _, b, _ := want.snapshot(); return strings.Contains(b, "\ndata:") })
	cancel()
	for _, d := range []<-chan struct{}{wantDone, reconDone, otherDone, defDone} {
		<-d
	}
	code, body, _ := want.snapshot()
	blocks := parseSSEBlocks(t, body)
	if code != 200 || len(blocks) != 1 || blocks[0].event != string(EventLaneHealthChanged) {
		t.Fatalf("filtered subscriber: code %d blocks %+v", code, blocks)
	}
	if !strings.Contains(blocks[0].data, `"lane_id":"lane-a"`) {
		t.Fatalf("payload: %s", blocks[0].data)
	}
	for name, w := range map[string]*syncRecorder{"other filter": recon, "other topic": other, "default": def} {
		if _, b, _ := w.snapshot(); strings.Contains(b, "data:") {
			t.Fatalf("%s received an event: %q", name, b)
		}
	}
}

// TestFleetTopicUnknownFilterIs400: a malformed or unknown filter is a 400
// before the handshake, and the topic is owner-only end to end.
func TestFleetTopicUnknownFilterIs400(t *testing.T) {
	withOwnerUID(t, 501)
	mux, _ := topologyMux(t)
	for _, f := range []string{
		"lane.health_changed;x", "Lane.Health_Changed", "lane.health_changed,bogus",
		"supervisor.escalation", "fleet.topology", "lane.health_changed%00",
	} {
		ctx, cancel := context.WithCancel(context.Background())
		w, done := runMux(ctx, mux, "?topic=fleet.topology&filter="+f)
		<-done
		cancel()
		code, body, _ := w.snapshot()
		if code != 400 || strings.Contains(body, "data:") {
			t.Fatalf("filter %q: code %d body %q, want 400", f, code, body)
		}
	}
	assertTopologyOwnerOnly(t)
}

// assertTopologyOwnerOnly checks the declaration and that a caller
// principal is refused while the owner CLI is admitted.
func assertTopologyOwnerOnly(t *testing.T) {
	t.Helper()
	accessMu.Lock()
	a, ok := topicAccess[FleetTopologyTopic]
	accessMu.Unlock()
	if !ok || a != AccessOwnerOnly {
		t.Fatalf("fleet.topology access = %v (declared %v), want AccessOwnerOnly", a, ok)
	}
	m, clock, token := principalFixture(t)
	var seen context.Context
	mux := NewSSEMux(newRecordingTopic((*SSEMux).ServeHTTP, new(context.Context))).
		WithTopic(FleetTopologyTopic, newRecordingTopic((*SSEMux).ServeHTTP, &seen))
	reg := NewRegistry()
	reg.SetAccessPolicy(func(tok string) (principal.Principal, error) { return m.Resolve(tok, clock.Now()) })
	h := NewHandlerWithSSE(reg, mux)
	if code := eventsRequest(h, "topic=fleet.topology", token, ""); code != 403 || seen != nil {
		t.Fatalf("principal on fleet.topology: code %d served %v, want 403", code, seen != nil)
	}
	if code := eventsRequest(h, "topic=fleet.topology", "", "owner-cli"); code != 200 || seen == nil {
		t.Fatalf("owner on fleet.topology: code %d served %v, want 200", code, seen != nil)
	}
}

// TestFleetTopologySSEWireFixture: the frames the real server writes for
// both events match the captured fixture byte for byte. Set
// CASCADE_UPDATE_FIXTURES=1 to recapture it from the real server.
func TestFleetTopologySSEWireFixture(t *testing.T) {
	withOwnerUID(t, 501)
	mux, bus := topologyMux(t)
	ctx, cancel := context.WithCancel(context.Background())
	w, done := runMux(ctx, mux, "?topic=fleet.topology")
	waitWrote(t, w)
	emitTopology(bus)
	waitFor(t, func() bool { _, b, _ := w.snapshot(); return strings.Count(b, "\ndata:") >= 2 })
	cancel()
	<-done
	code, body, _ := w.snapshot()
	if code != 200 || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("handshake: code %d content-type %q", code, w.Header().Get("Content-Type"))
	}
	path := filepath.Join("testdata", fleetTopologyFixture)
	if os.Getenv("CASCADE_UPDATE_FIXTURES") == "1" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if body != string(want) {
		t.Fatalf("wire frames differ from %s:\ngot:\n%s\nwant:\n%s", path, body, want)
	}
	blocks := parseSSEBlocks(t, body)
	if len(blocks) != 2 || blocks[0].event != string(EventLaneHealthChanged) ||
		blocks[1].event != string(EventFleetTopologyReconciled) {
		t.Fatalf("fixture events: %+v", blocks)
	}
}
