package plugins

// Purpose (this file): proves the bridge journal's approval-refusal sink,
//   EmitApprovalUnauthorized (P1-E23-W5-S48-T4), publishes exactly one typed
//   bridge.approval.unauthorized record carrying the event's own fields and
//   nothing else, and that its marshal failure publishes nothing. Without
//   this, a non-owner tap's R-21.210 STEP 0 rejection record was asserted by
//   no test: the handler tests use their own recording sink.
//
// Inputs: newRecordingBus (cascadepa_bridge_events_test.go).
//
// Outputs: none beyond test assertions.
//
// Constraints: no network, no keychain, no home directory.
//
// SPORT: internal/plugins:cascadepa-bridge-events/TEST (P1-E23-W5-S48-T4).

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/plugins/cascade-pa/telegram"
)

// TestBridgeJournal_RecordsAnUnauthorizedApproval drives the real journal
// over a recording bus and decodes what it published.
func TestBridgeJournal_RecordsAnUnauthorizedApproval(t *testing.T) {
	bus := newRecordingBus()
	at := time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC)
	newBridgeJournal(bus).EmitApprovalUnauthorized(context.Background(), telegram.ApprovalUnauthorizedEvent{
		Subject: "bridge-1", SenderID: "4242", ChatKind: "group", At: at,
	})
	got := bus.events()
	if len(got) != 1 {
		t.Fatalf("recorded %d events, want 1", len(got))
	}
	if got[0].kind != EventKindBridgeApprovalUnauthorized || string(got[0].kind) != telegram.ApprovalUnauthorizedKind {
		t.Fatalf("recorded kind %q, want %q", got[0].kind, telegram.ApprovalUnauthorizedKind)
	}
	if got[0].namespace != bridgeEventNamespace {
		t.Fatalf("recorded namespace %q, want %q", got[0].namespace, bridgeEventNamespace)
	}
	var body telegram.ApprovalUnauthorizedEvent
	if err := json.Unmarshal([]byte(got[0].payload), &body); err != nil {
		t.Fatalf("payload is not decodable JSON: %v", err)
	}
	if body.Subject != "bridge-1" || body.SenderID != "4242" || body.ChatKind != "group" || !body.At.Equal(at) {
		t.Fatalf("payload = %+v", body)
	}
}

// TestBridgeJournal_UnauthorizedApprovalMarshalFailureDoesNotPublish covers
// the marshal branch through its declared test hook, as the quarantine sink's
// test does (cascadepa_bridge_coverage_test.go).
func TestBridgeJournal_UnauthorizedApprovalMarshalFailureDoesNotPublish(t *testing.T) {
	bus := newRecordingBus()
	orig := marshalApprovalUnauthorizedEvent
	marshalApprovalUnauthorizedEvent = func(any) ([]byte, error) { return nil, errors.New("test: marshal failed") }
	defer func() { marshalApprovalUnauthorizedEvent = orig }()
	newBridgeJournal(bus).EmitApprovalUnauthorized(context.Background(), telegram.ApprovalUnauthorizedEvent{})
	if len(bus.events()) != 0 {
		t.Fatal("a marshal failure still published a record")
	}
}
