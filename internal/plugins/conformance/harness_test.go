// Package conformance (harness_test.go): Purpose: ABIHarness is the
// shared seam every TestConformance_* test in suite_test.go drives: one
// fixture, dispatched through a real plugin runtime, normalised to an
// ABIObservation the suite can compare across runtimes.
//
// Inputs: a HostFnFixture (fixture_runner_test.go) naming the logical ABI
// v1 host function and its happy- or error-path request.
//
// Outputs: an ABIObservation -- a full response payload for a runtime
// that has one (builtin, wasm), or a capability-boundary decision only
// for the one that today does not (process -- see harness_process_test.go's
// doc comment for the real reason, a genuine tree finding this ticket
// records rather than works around).
//
// Constraints: every file in this package is a _test.go file
// (12-QUALITY-CONSTITUTION.md Art.10.5 -- see suite_test.go's package
// doc); no exported symbol from this package ships in the binary.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2).
package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ABIObservation is the normalised outcome of one ABIHarness.Call.
type ABIObservation struct {
	// ResponseJSON is the ABI v1 response payload, normalised to JSON,
	// for a full-response runtime (builtin, wasm). Nil for a
	// capability-only runtime (process).
	ResponseJSON json.RawMessage
	// HasErr is true when the call returned an error.
	HasErr bool
	// ErrKind is the frozen pkg/cascade Kind name (Kind.String(), e.g.
	// "not-found") of the returned error. Empty when HasErr is false, or
	// when the error did not carry a taxonomy Kind.
	ErrKind string
	// CapabilityOnly mirrors the harness's own CapabilityOnly() for a
	// caller holding only the observation.
	CapabilityOnly bool
	// Allowed is meaningful only when CapabilityOnly is true: whether the
	// boundary allowed the call.
	Allowed bool
	// Observed reports whether a capability-only harness produced any
	// boundary decision at all. False means the call was not gated by
	// this runtime today -- see host_log's real, documented divergence
	// on the process runtime (hostcalls.go never checks it).
	Observed bool
}

// cascadeKindNames lists the frozen taxonomy's stable Kind.String()
// values (pkg/cascade/kinds.go's kindNames), used by errKindOf's fallback
// below.
var cascadeKindNames = []string{
	"not-found", "invalid-input", "conflict", "unavailable", "timeout", "canceled",
	"permission-denied", "elevation-required", "policy-denied", "capability-denied",
	"quota-exhausted", "unsupported", "integrity", "internal",
}

// errKindOf reports err's frozen Kind name.
//
// GENUINE ABI FINDING (LANE-RULES §1/§4), recorded here rather than
// worked around: a *cascade.Error's typed Kind does NOT survive a real
// wasm guest call. wasm/runtime.go's dispatchCall reads a guest-returned
// result{Error: err.Error()} string and reconstructs it as
// `fmt.Errorf("%s", res.Error)` -- a plain error, never a *cascade.Error
// -- so cascade.KindOf on anything wasmHarness returns always reports
// ok=false. errKindOf falls back to parsing the Kind's own stable
// "<kind>: " string prefix (the exact text Error.Error() produces,
// pkg/cascade/errors.go) out of the message, which the wasm boundary
// DOES preserve as plain text even though it discards the type. This
// keeps TestConformance_AllRuntimesAgree's builtin-vs-wasm ErrKind
// comparison meaningful; it does not paper over the underlying gap,
// which host_abi_core.go/runtime.go's dispatchCall would need to fix and
// which is outside this ticket's files_scope.change.
func errKindOf(err error) string {
	if err == nil {
		return ""
	}
	if k, ok := cascade.KindOf(err); ok {
		return k.String()
	}
	msg := err.Error()
	for _, name := range cascadeKindNames {
		if strings.HasPrefix(msg, name+":") {
			return name
		}
	}
	return ""
}

// ABIHarness drives one ABI v1 host-function fixture through a real
// plugin runtime.
type ABIHarness interface {
	// Name identifies the runtime in a failing assertion's message (Rule
	// 4: mutation-prove wiring, name the runtime that diverged).
	Name() string
	// CapabilityOnly reports whether this harness can only observe a
	// capability-boundary decision, never a full ABI response. Callers
	// must not compare ResponseJSON across a CapabilityOnly harness and
	// a full-response one.
	CapabilityOnly() bool
	// Call dispatches f's request through the real runtime and returns
	// the normalised observation.
	Call(ctx context.Context, t *testing.T, f HostFnFixture) ABIObservation
}

// builtinHarness calls refSink directly: a builtin plugin is compiled
// into the host binary and crosses no serialisation boundary, so its
// host-fn "call" is a direct Go call into the same delegate the wasm
// runtime's handlers use.
type builtinHarness struct {
	sink *refSink
}

func newBuiltinHarness() *builtinHarness { return &builtinHarness{sink: newRefSink()} }

func (h *builtinHarness) Name() string         { return "builtin" }
func (h *builtinHarness) CapabilityOnly() bool { return false }

// Call implements ABIHarness by decoding f's wasm-shaped request JSON
// (the ABI v1 wire shape every runtime's request ultimately carries) and
// invoking the matching refSink method.
func (h *builtinHarness) Call(ctx context.Context, t *testing.T, f HostFnFixture) ABIObservation {
	t.Helper()
	resp, err := callRefSinkMethod(ctx, h.sink, f)
	return observationFromRefSink(resp, err)
}

// observationFromRefSink builds the shared full-response ABIObservation
// both builtinHarness and wasmHarness produce from a refSink-shaped
// result, so the two comparison legs share one normalisation path.
func observationFromRefSink(resp any, err error) ABIObservation {
	if err != nil {
		return ABIObservation{HasErr: true, ErrKind: errKindOf(err)}
	}
	data, mErr := json.Marshal(resp)
	if mErr != nil {
		return ABIObservation{HasErr: true, ErrKind: "internal"}
	}
	return ABIObservation{ResponseJSON: data}
}
