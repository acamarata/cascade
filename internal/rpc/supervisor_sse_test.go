package rpc

// Purpose (this file): KnownSupervisorEventKind/SupervisorEventEmitter unit
// coverage, plus TestSupervisorSSEFixtureCapture (captures
// testdata/supervisor-sse-fixture.ndjson from a REAL SSEHandler over a
// REAL events.Bus — Art.2, mirroring internal/fleet/top_test.go's own
// identical top-sse-fixture.ndjson precedent) and TestSupervisorSSEReplay
// (task 5: a unit test — no daemon required — that replays the recorded
// corpus). See testdata/README.md for full provenance.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
)

func TestKnownSupervisorEventKind(t *testing.T) {
	for _, kind := range supervisorEventKinds {
		if !KnownSupervisorEventKind(kind) {
			t.Errorf("KnownSupervisorEventKind(%q) = false, want true", kind)
		}
	}
	if KnownSupervisorEventKind(events.EventKind("not.a.supervisor.kind")) {
		t.Error("KnownSupervisorEventKind matched an unregistered kind")
	}
}

func TestSupervisorEventEmitter_NilBus_NoPanic(_ *testing.T) {
	// Must not panic: a nil bus (embedded/daemonless mode) is documented
	// as a no-op, mirroring capacity.WireSSE's identical convention.
	e := NewSupervisorEventEmitter(nil)
	e.EmitAttentionAdded(context.Background(), SupervisorAttentionAddedPayload{ItemID: "x"})
	e.EmitStallDetected(context.Background(), SupervisorStallDetectedPayload{SessionID: "x"})
	e.EmitEscalation(context.Background(), SupervisorEscalationPayload{SessionID: "x"})
	e.EmitHeadroomUpdate(context.Background(), SupervisorHeadroomUpdatePayload{Resource: "x"})

	var nilEmitter *SupervisorEventEmitter
	nilEmitter.EmitAttentionAdded(context.Background(), SupervisorAttentionAddedPayload{})
}

// TestSupervisorEventEmitter_StampsSchemaVersion proves every Emit* method
// stamps SchemaVersion on the published payload even when the caller left
// it zero.
func TestSupervisorEventEmitter_StampsSchemaVersion(t *testing.T) {
	bus := &fakeSupervisorBus{}
	e := NewSupervisorEventEmitter(bus)
	e.EmitHeadroomUpdate(context.Background(), SupervisorHeadroomUpdatePayload{Resource: "inflight", EnforcedCeiling: 8, Ratio: 0.5})

	if len(bus.published) != 1 {
		t.Fatalf("published count = %d, want 1", len(bus.published))
	}
	var got SupervisorHeadroomUpdatePayload
	if err := json.Unmarshal(bus.published[0].payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SchemaVersion != SupervisorSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, SupervisorSchemaVersion)
	}
	if bus.published[0].namespace != supervisorEventNamespace {
		t.Errorf("namespace = %q, want %q (the daemon's one wired SSEHandler namespace)", bus.published[0].namespace, supervisorEventNamespace)
	}
	if bus.published[0].kind != EventSupervisorHeadroomUpdate {
		t.Errorf("kind = %q, want %q", bus.published[0].kind, EventSupervisorHeadroomUpdate)
	}
}

type publishedCall struct {
	namespace string
	kind      events.EventKind
	payload   json.RawMessage
}

type fakeSupervisorBus struct {
	published []publishedCall
}

func (f *fakeSupervisorBus) Publish(_ context.Context, namespace string, kind events.EventKind, _ string, payload []byte) (events.Event, error) {
	f.published = append(f.published, publishedCall{namespace: namespace, kind: kind, payload: json.RawMessage(payload)})
	return events.Event{Seq: uint64(len(f.published))}, nil
}

// TestSupervisorSSEFixtureCapture captures
// testdata/supervisor-sse-fixture.ndjson from a REAL SSEHandler bound to a
// REAL events.Bus (newTestBus, sse_test.go), driving all four Emit*
// methods through SupervisorEventEmitter over a real HTTP round trip
// (runSSE) — the exact real-handler/real-bus/synthetic-Publish-call
// pattern testdata/README.md's provenance note and
// internal/fleet/testdata/README.md's own top-sse-fixture.ndjson already
// establish for this identical situation. Run this test to regenerate the
// fixture.
func TestSupervisorSSEFixtureCapture(t *testing.T) {
	bus, clock := newTestBus()
	defer func() { _ = bus.Close() }()
	h := NewSSEHandler(bus, "daemon", KnownSupervisorEventKind, clock)

	ctx, cancel := context.WithCancel(context.Background())
	w, done := runSSE(ctx, h, "", "")
	waitFor(t, func() bool { _, _, wrote := w.snapshot(); return wrote })

	emitter := NewSupervisorEventEmitter(bus)
	emitter.EmitAttentionAdded(context.Background(), SupervisorAttentionAddedPayload{
		ItemID: "attn-001", Kind: "policy-ask", ScopeKind: "session", ScopeID: "sess-alpha",
	})
	emitter.EmitStallDetected(context.Background(), SupervisorStallDetectedPayload{
		SessionID: "sess-alpha", StallKind: "no-progress",
	})
	emitter.EmitEscalation(context.Background(), SupervisorEscalationPayload{
		SessionID: "sess-alpha", Rung: "supervisor-task",
	})
	emitter.EmitHeadroomUpdate(context.Background(), SupervisorHeadroomUpdatePayload{
		Resource: "inflight", EnforcedCeiling: 8, Ratio: 0.375,
	})

	waitFor(t, func() bool {
		_, body, _ := w.snapshot()
		return strings.Count(body, "\ndata:") >= 4
	})
	cancel()
	<-done

	_, body, _ := w.snapshot()
	if err := os.WriteFile(filepath.Join("testdata", "supervisor-sse-fixture.ndjson"), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

// sseBlock is one parsed SSE record in internal/rpc/sse.go's real wire
// envelope: "id: <token>\ndata: {\"seq\":N,\"kind\":...,\"source\":...,
// \"payload\":<base64>}\n\n" (writeSSEEvent/sseEventJSON) — NOT the
// "event:"-line shape internal/fleet/sessions' own, separate SSEHandler
// uses (top-sse-fixture.ndjson). event is the decoded envelope's "kind"
// field; data is the base64-decoded payload JSON.
type sseBlock struct {
	event string
	data  string
}

var sseDataLine = regexp.MustCompile(`^data: (.*)$`)

// sseEnvelope mirrors sse.go's sseEventJSON output shape.
type sseEnvelope struct {
	Seq     uint64 `json:"seq"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Payload string `json:"payload"` // base64, per sseEventJSON
}

// parseSSEBlocks splits raw SSE wire text (this package's real
// writeSSEEvent output) into its individual event records, decoding each
// envelope's base64 payload back to plain JSON.
func parseSSEBlocks(t *testing.T, raw string) []sseBlock {
	t.Helper()
	var blocks []sseBlock
	for _, line := range strings.Split(raw, "\n") {
		m := sseDataLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		var env sseEnvelope
		if err := json.Unmarshal([]byte(m[1]), &env); err != nil {
			t.Fatalf("unmarshal SSE envelope %q: %v", m[1], err)
		}
		payload, err := base64.StdEncoding.DecodeString(env.Payload)
		if err != nil {
			t.Fatalf("base64-decode payload for %q: %v", env.Kind, err)
		}
		blocks = append(blocks, sseBlock{event: env.Kind, data: string(payload)})
	}
	return blocks
}

// TestSupervisorSSEReplay replays the recorded corpus
// (testdata/supervisor-sse-fixture.ndjson) and proves all four event
// types are present, each decoding with a non-zero schema_version — a
// pure unit test: no daemon, no bus, no HTTP round trip.
func TestSupervisorSSEReplay(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "supervisor-sse-fixture.ndjson"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	blocks := parseSSEBlocks(t, string(raw))

	seen := map[string]bool{
		string(EventSupervisorAttentionAdded): false,
		string(EventSupervisorStallDetected):  false,
		string(EventSupervisorEscalation):     false,
		string(EventSupervisorHeadroomUpdate): false,
	}
	if len(blocks) != len(seen) {
		t.Fatalf("len(blocks) = %d, want %d", len(blocks), len(seen))
	}
	for _, b := range blocks {
		if _, known := seen[b.event]; !known {
			t.Fatalf("unexpected event kind %q in fixture", b.event)
		}
		seen[b.event] = true
		var schemaVersion struct {
			SchemaVersion int `json:"schema_version"`
		}
		if err := json.Unmarshal([]byte(b.data), &schemaVersion); err != nil {
			t.Fatalf("unmarshal data for %q: %v", b.event, err)
		}
		if schemaVersion.SchemaVersion != SupervisorSchemaVersion {
			t.Errorf("%s: schema_version = %d, want %d", b.event, schemaVersion.SchemaVersion, SupervisorSchemaVersion)
		}
	}
	for kind, ok := range seen {
		if !ok {
			t.Errorf("event kind %q missing from fixture", kind)
		}
	}

	assertReplayPayload(t, blocks, EventSupervisorAttentionAdded, &SupervisorAttentionAddedPayload{})
	assertReplayPayload(t, blocks, EventSupervisorStallDetected, &SupervisorStallDetectedPayload{})
	assertReplayPayload(t, blocks, EventSupervisorEscalation, &SupervisorEscalationPayload{})
	assertReplayPayload(t, blocks, EventSupervisorHeadroomUpdate, &SupervisorHeadroomUpdatePayload{})
}

// assertReplayPayload finds the block matching kind and decodes its data
// into dst, failing the test on a decode error.
func assertReplayPayload(t *testing.T, blocks []sseBlock, kind events.EventKind, dst any) {
	t.Helper()
	for _, b := range blocks {
		if b.event != string(kind) {
			continue
		}
		if err := json.Unmarshal([]byte(b.data), dst); err != nil {
			t.Fatalf("unmarshal %s payload: %v", kind, err)
		}
		return
	}
	t.Fatalf("no block found for kind %q", kind)
}
