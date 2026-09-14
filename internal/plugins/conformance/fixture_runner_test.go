// Package conformance (fixture_runner_test.go): Purpose: HostFnFixture is
// the recorded conformance fixture schema (05-PEWS-PLAN-W4-W6.md
// §S-32.T2's "fixture schema" task); loadFixtures reads the recorded set
// from testdata/fixtures, and callRefSinkMethod is the one dispatch table
// both builtinHarness and wasmHarness's expected-response leg route a
// fixture's request through, so the two share byte-identical response
// construction (harness_test.go's doc comment explains why that makes
// TestConformance_AllRuntimesAgree a real proof, not a tautology).
//
// Inputs: testdata/fixtures/abi_v1.json, one JSON array of fixture
// entries.
//
// Outputs: []HostFnFixture for suite_test.go's table-driven tests.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2).
package conformance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The seven ABI v1 host-function names, matching fixture.wasm's real
// exported wrapper names (testdata/README.md) with the "call_" prefix
// stripped. host_storage collapses get/put/delete/list into ONE entry,
// matching 05-PEWS-PLAN-W4-W6.md's own host-fn list, which names
// "host_storage_get/put/delete/list" as a single list item.
const (
	methodLog          = "host_log"
	methodStorage      = "host_storage"
	methodHTTP         = "host_http"
	methodStream       = "host_stream"
	methodSecretRef    = "host_secretref"
	methodEventEmit    = "host_eventemit"
	methodToolRegister = "host_toolregister"
)

// allABIMethods enumerates the seven ABI v1 host functions in the order
// the fixture set must cover.
var allABIMethods = []string{
	methodLog, methodStorage, methodHTTP, methodStream,
	methodSecretRef, methodEventEmit, methodToolRegister,
}

// wasmGuestExport maps a logical ABI method to fixture.wasm's real
// exported wrapper name.
var wasmGuestExport = map[string]string{
	methodLog: "call_host_log", methodStorage: "call_host_storage",
	methodHTTP: "call_host_http", methodStream: "call_host_stream",
	methodSecretRef: "call_host_secretref", methodEventEmit: "call_host_eventemit",
	methodToolRegister: "call_host_toolregister",
}

// HostFnFixture is one recorded conformance entry: a logical ABI v1
// host-fn call, its wasm-shaped request/response wire bytes (the shape
// every runtime's request ultimately carries), and, where the process
// runtime's real (narrower) wire shape and capability gate differ, the
// literal Notification frame it would receive.
type HostFnFixture struct {
	Method      string          `json:"method"`
	Kind        string          `json:"kind"` // "happy" | "error"
	WasmRequest json.RawMessage `json:"wasm_request"`
	// ExpectErrKind is set only for an error-path fixture: the frozen
	// pkg/cascade Kind name (Kind.String()) the call must fail with.
	ExpectErrKind string `json:"expect_err_kind,omitempty"`

	// ProcessMethod is the real process.Notification method name
	// (hostcalls.go) this fixture maps to, empty when the process
	// runtime never observes this ABI method at all (see host_log's
	// documented divergence).
	ProcessMethod string `json:"process_method,omitempty"`
	// ProcessParams is the literal Notification.Params JSON the process
	// runtime's real wire shape carries for this call -- genuinely
	// different field names than WasmRequest for host_storage/http/
	// secretref (decodeHostParam reads "domain"/"url"/"key", never the
	// wasm ABI's op/key/value -- a real, documented wire divergence).
	ProcessParams json.RawMessage `json:"process_params,omitempty"`
	// ExpectProcessAllowed is nil when ProcessMethod is empty (not
	// gated); otherwise the capability-boundary decision this fixture's
	// grants must produce.
	ExpectProcessAllowed *bool `json:"expect_process_allowed,omitempty"`
	// ProcessGrantProfile selects which fixed grant profile
	// harness_process_test.go's Call builds the real HostBoundaryEnforcer
	// from: "granted" (full net/storage/policy/broker access) or
	// "denied" (zero grants, denying policy, denying broker). Required
	// whenever ProcessMethod is set.
	ProcessGrantProfile string `json:"process_grant_profile,omitempty"`

	Note string `json:"note,omitempty"`
}

// loadFixtures reads the recorded ABI v1 fixture set.
func loadFixtures(t *testing.T) []HostFnFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "abi_v1.json"))
	if err != nil {
		t.Fatalf("read fixture set: %v", err)
	}
	var fixtures []HostFnFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("decode fixture set: %v", err)
	}
	return fixtures
}

// TestFixtureSet_CoversAllHostFnsWithHappyAndErrorPairs is this ticket's
// own CI gate for fixture completeness (task 6's "fail on missing
// happy/error pair for any host-fn"): every one of the seven ABI v1
// host functions must have at least one happy-path and one error-path
// entry. Runs in the default lane, so a fixture regression fails
// go test, not only the dedicated CI job.
func TestFixtureSet_CoversAllHostFnsWithHappyAndErrorPairs(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures) < 14 {
		t.Fatalf("fixture set has %d entries, want >= 14 (7 host-fns x happy+error)", len(fixtures))
	}
	seen := map[string]map[string]bool{}
	for _, f := range fixtures {
		if seen[f.Method] == nil {
			seen[f.Method] = map[string]bool{}
		}
		seen[f.Method][f.Kind] = true
	}
	for _, m := range allABIMethods {
		if !seen[m]["happy"] {
			t.Errorf("host-fn %q has no happy-path fixture entry", m)
		}
		if !seen[m]["error"] {
			t.Errorf("host-fn %q has no error-path fixture entry", m)
		}
	}
}

// callRefSinkMethod decodes f's wasm-shaped request and invokes the
// matching refSink method, returning a response value of the SAME
// wasm.*Response type wasmHarness decodes a real guest call's result
// into -- the shared shape that makes a JSON comparison between the two
// harnesses meaningful.
func callRefSinkMethod(ctx context.Context, sink *refSink, f HostFnFixture) (any, error) {
	switch f.Method {
	case methodLog:
		return callRefSinkLog(ctx, sink, f)
	case methodStorage:
		return callRefSinkStorage(ctx, sink, f)
	case methodHTTP:
		return callRefSinkHTTP(ctx, sink, f)
	case methodStream:
		return callRefSinkStream(ctx, sink, f)
	case methodSecretRef:
		return callRefSinkSecretRef(ctx, sink, f)
	case methodEventEmit:
		return callRefSinkEventEmit(ctx, sink, f)
	case methodToolRegister:
		return callRefSinkToolRegister(ctx, sink, f)
	default:
		return nil, errUnknownMethod(f.Method)
	}
}
