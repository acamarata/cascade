package rpc

// Purpose (this file): real-entry-point tests for supervisor.snapshot and
// supervisor.events_schema, driven through a real *Registry.Dispatch
// (Art.2's real counterpart, matching internal/fleet/capacity/rpc_test.go's
// identical precedent). See testdata/README.md for
// supervisor-rpc-fixture.json's provenance.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeSupervisorSource is a real (non-nil, non-mock-framework) implementer
// of SupervisorSource, holding fixed data for tests to assert against.
type fakeSupervisorSource struct {
	result SupervisorSnapshotResult
	err    error
}

func (f *fakeSupervisorSource) Snapshot(_ context.Context) (SupervisorSnapshotResult, error) {
	return f.result, f.err
}

func headroomCeiling(v int64) *int64 { return &v }

func populatedSnapshot() SupervisorSnapshotResult {
	return SupervisorSnapshotResult{
		SchemaVersion:       SupervisorSchemaVersion,
		AttentionQueueDepth: 3,
		StallCount:          2,
		Sessions: []SupervisorSessionCounts{
			{SessionID: "sess-alpha", ActionCount: 14, InterruptCount: 2},
			{SessionID: "sess-beta", ActionCount: 5, InterruptCount: 1},
		},
		HeadroomCeiling: headroomCeiling(8),
		AutonomyProfile: "balanced",
		AutoAdvanceTier: "L1",
	}
}

// TestSupervisorSnapshotDaemonRequired proves task 7's refusal: a nil
// SupervisorSource (the daemon's supervision surface not running) answers
// supervisor.snapshot with a taxonomy KindUnavailable error over a real
// Dispatch call — never a panic, never a nil/zero-value result read as
// "confirmed empty".
func TestSupervisorSnapshotDaemonRequired(t *testing.T) {
	reg := NewRegistry()
	RegisterSupervisorHandlers(reg, nil)

	result, errObj := reg.Dispatch(context.Background(), &Request{JSONRPC: "2.0", Method: MethodSupervisorSnapshot})
	if errObj == nil {
		t.Fatalf("Dispatch(supervisor.snapshot) = %v, nil, want a KindUnavailable refusal", result)
	}
	if errObj.Code != cascade.RPCCodeUnavailable {
		t.Errorf("errObj.Code = %d, want %d (RPCCodeUnavailable)", errObj.Code, cascade.RPCCodeUnavailable)
	}
}

// TestSupervisorSnapshot_RealSource_FullyPopulated proves a real source's
// full struct passes through the handler unchanged (Art.1: no stub
// collapses a non-zero result down to zero anywhere in this path).
func TestSupervisorSnapshot_RealSource_FullyPopulated(t *testing.T) {
	reg := NewRegistry()
	src := &fakeSupervisorSource{result: populatedSnapshot()}
	RegisterSupervisorHandlers(reg, src)

	result, errObj := reg.Dispatch(context.Background(), &Request{JSONRPC: "2.0", Method: MethodSupervisorSnapshot})
	if errObj != nil {
		t.Fatalf("Dispatch(supervisor.snapshot) error = %+v", errObj)
	}
	snap, ok := result.(SupervisorSnapshotResult)
	if !ok {
		t.Fatalf("result type = %T, want SupervisorSnapshotResult", result)
	}
	if snap.AttentionQueueDepth == 0 || snap.StallCount == 0 || len(snap.Sessions) == 0 ||
		snap.HeadroomCeiling == nil || snap.AutonomyProfile == "" || snap.AutoAdvanceTier == "" {
		t.Fatalf("snapshot has a zero/empty field it should not: %+v", snap)
	}
}

// TestSupervisorSnapshot_SourceError_Propagates proves a real storage
// error from the source fails closed — never masked into a zero-value
// success.
func TestSupervisorSnapshot_SourceError_Propagates(t *testing.T) {
	reg := NewRegistry()
	src := &fakeSupervisorSource{err: cascade.New(cascade.KindInternal, "boom")}
	RegisterSupervisorHandlers(reg, src)

	_, errObj := reg.Dispatch(context.Background(), &Request{JSONRPC: "2.0", Method: MethodSupervisorSnapshot})
	if errObj == nil {
		t.Fatal("expected the source's error to propagate, got none")
	}
}

// TestDecodeSupervisorSnapshotParams_UnknownField_Rejected proves the wire
// shape is the allow-list: an unknown field is a typed KindInvalidInput
// refusal, never silently ignored.
func TestDecodeSupervisorSnapshotParams_UnknownField_Rejected(t *testing.T) {
	err := decodeSupervisorSnapshotParams(json.RawMessage(`{"unexpected":1}`))
	if err == nil {
		t.Fatal("expected a refusal for an unknown field, got none")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		kind, _ := cascade.KindOf(err)
		t.Errorf("error kind = %v, want KindInvalidInput", kind)
	}
}

// TestDecodeSupervisorSnapshotParams_EmptyIsValid proves absent/empty
// params (the normal case) decode cleanly.
func TestDecodeSupervisorSnapshotParams_EmptyIsValid(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{}`)} {
		if err := decodeSupervisorSnapshotParams(raw); err != nil {
			t.Errorf("decodeSupervisorSnapshotParams(%q) = %v, want nil", raw, err)
		}
	}
}

// TestSupervisorEventsSchema_FourKindsWithSchemaVersion proves
// supervisor.events_schema describes all four SSE event kinds, each
// carrying schema_version among its fields.
func TestSupervisorEventsSchema_FourKindsWithSchemaVersion(t *testing.T) {
	reg := NewRegistry()
	RegisterSupervisorHandlers(reg, nil) // events_schema needs no source

	result, errObj := reg.Dispatch(context.Background(), &Request{JSONRPC: "2.0", Method: MethodSupervisorEventsSchema})
	if errObj != nil {
		t.Fatalf("Dispatch(supervisor.events_schema) error = %+v", errObj)
	}
	doc, ok := result.(SupervisorEventsSchemaResult)
	if !ok {
		t.Fatalf("result type = %T, want SupervisorEventsSchemaResult", result)
	}
	if doc.SchemaVersion != SupervisorSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", doc.SchemaVersion, SupervisorSchemaVersion)
	}
	want := map[string]bool{
		string(EventSupervisorAttentionAdded): false,
		string(EventSupervisorStallDetected):  false,
		string(EventSupervisorEscalation):     false,
		string(EventSupervisorHeadroomUpdate): false,
	}
	if len(doc.Events) != len(want) {
		t.Fatalf("len(Events) = %d, want %d", len(doc.Events), len(want))
	}
	for _, entry := range doc.Events {
		if _, known := want[entry.Kind]; !known {
			t.Errorf("unexpected kind %q in schema doc", entry.Kind)
			continue
		}
		want[entry.Kind] = true
		hasVersion := false
		for _, f := range entry.Fields {
			if f.Name == "schema_version" {
				hasVersion = true
			}
		}
		if !hasVersion {
			t.Errorf("kind %q has no schema_version field", entry.Kind)
		}
	}
	for kind, seen := range want {
		if !seen {
			t.Errorf("kind %q missing from schema doc", kind)
		}
	}
}

// TestRegisterSupervisorHandlers_ReachableViaDispatch is the wiring
// mutation proof: both methods must be registered by name, and answer
// something other than JSON-RPC's method-not-found.
func TestRegisterSupervisorHandlers_ReachableViaDispatch(t *testing.T) {
	reg := NewRegistry()
	for _, method := range []string{MethodSupervisorSnapshot, MethodSupervisorEventsSchema} {
		if reg.Registered(method) {
			t.Fatalf("%s registered before RegisterSupervisorHandlers ran", method)
		}
	}
	RegisterSupervisorHandlers(reg, &fakeSupervisorSource{result: populatedSnapshot()})
	for _, method := range []string{MethodSupervisorSnapshot, MethodSupervisorEventsSchema} {
		if !reg.Registered(method) {
			t.Errorf("%s never reached the registry", method)
		}
	}
	const methodNotFound = -32601
	for _, method := range []string{MethodSupervisorSnapshot, MethodSupervisorEventsSchema} {
		_, errObj := reg.Dispatch(context.Background(), &Request{JSONRPC: "2.0", Method: method})
		if errObj != nil && errObj.Code == methodNotFound {
			t.Errorf("%s returned method-not-found: %+v", method, errObj)
		}
	}
}

// TestSupervisorRPCFixture regenerates testdata/supervisor-rpc-fixture.json
// from a real *Registry.Dispatch call over a fixed, real (non-mock)
// SupervisorSource — see testdata/README.md for full provenance. Run this
// test to regenerate the fixture.
func TestSupervisorRPCFixture(t *testing.T) {
	reg := NewRegistry()
	RegisterSupervisorHandlers(reg, &fakeSupervisorSource{result: populatedSnapshot()})

	req := &Request{JSONRPC: "2.0", Method: MethodSupervisorSnapshot, ID: json.RawMessage(`1`)}
	result, errObj := reg.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodSupervisorSnapshot, errObj)
	}
	snap, ok := result.(SupervisorSnapshotResult)
	if !ok {
		t.Fatalf("result type = %T, want SupervisorSnapshotResult", result)
	}

	respBytes, err := json.Marshal(struct {
		Request json.RawMessage          `json:"request"`
		Result  SupervisorSnapshotResult `json:"result"`
	}{Request: mustMarshalSupervisor(t, req), Result: snap})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	writeSupervisorFixture(t, "supervisor-rpc-fixture.json", respBytes)
}

func mustMarshalSupervisor(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// writeSupervisorFixture writes b to testdata/name, pretty-printed. See
// testdata/README.md for provenance.
func writeSupervisorFixture(t *testing.T, name string, b []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	var pretty map[string]any
	if err := json.Unmarshal(b, &pretty); err != nil {
		t.Fatalf("unmarshal for pretty-print: %v", err)
	}
	out, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		t.Fatalf("marshal indent: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
