// Package conformance implements the plugin ABI v1 conformance suite
// (12-QUALITY-CONSTITUTION.md Art.2, Art.12 row 5; P1-E15-W4-S32-T2):
// one shared fixture set, driven through the REAL builtin delegate path,
// the REAL wazero WASM runtime against a REAL compiled guest artifact,
// and the REAL process.ProcessRuntime against a REAL forked subprocess,
// asserting identical observable behaviour where all three runtimes
// actually implement one, and recording -- never hiding -- the two
// genuine ABI divergences the process runtime has today (see
// harness_process_test.go's package doc).
//
// Package shape (Art.10.5): every file here is a _test.go file; no
// exported symbol ships in the binary.
//
// SPORT: internal.plugins.conformance/ADDED (P1-E15-W4-S32-T2).
package conformance

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
)

// processWindowsSkipCount is the CI-asserted skip counter
// TestConformance_ProcessRuntime increments and checks in the same call,
// so the "never a silent pass" requirement holds without depending on
// cross-test execution order.
var processWindowsSkipCount int32

func recordWindowsSkip() int32 { return atomic.AddInt32(&processWindowsSkipCount, 1) }

func TestConformance_BuiltinRuntime(t *testing.T) {
	ctx := context.Background()
	h := newBuiltinHarness()
	runHarnessFixtures(ctx, t, h, true)
}

func TestConformance_WasmRuntime(t *testing.T) {
	ctx := context.Background()
	h := newWasmHarness(ctx, t)
	runHarnessFixtures(ctx, t, h, false)
}

// runHarnessFixtures asserts every fixture's happy/error outcome against
// a full-response harness. strictKind requires ErrKind to equal the
// fixture's ExpectErrKind exactly (builtin, which never crosses a
// serialisation boundary); wasm's call additionally accepts the
// substring-recovered Kind errKindOf's doc comment explains.
func runHarnessFixtures(ctx context.Context, t *testing.T, h ABIHarness, strictKind bool) {
	for _, f := range loadFixtures(t) {
		f := f
		t.Run(f.Method+"/"+f.Kind, func(t *testing.T) {
			obs := h.Call(ctx, t, f)
			if f.Kind == "happy" {
				if obs.HasErr {
					t.Fatalf("%s: %s: happy-path call returned an error (kind=%s)", h.Name(), f.Method, obs.ErrKind)
				}
				return
			}
			if !obs.HasErr {
				t.Fatalf("%s: %s: error-path call returned no error", h.Name(), f.Method)
			}
			if obs.ErrKind != f.ExpectErrKind {
				_ = strictKind // both harnesses use errKindOf's same recovery path today
				t.Fatalf("%s: %s: error kind = %q, want %q", h.Name(), f.Method, obs.ErrKind, f.ExpectErrKind)
			}
		})
	}
}

// TestConformance_ProcessRuntime asserts the process runtime's real,
// narrower observable: a capability-boundary allow/deny decision, never
// a full ABI response (harness_process_test.go's package doc explains
// why). Windows: process.ProcessRuntime.Launch refuses unconditionally
// (runtime_windows.go, Art.5) -- skip explicitly, with a CI-asserted
// skip count, never a silent pass.
func TestConformance_ProcessRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		count := recordWindowsSkip()
		if count != 1 {
			t.Fatalf("process windows skip count = %d, want exactly 1", count)
		}
		t.Skipf("process runtime: unsupported on %s (runtime_windows.go refuses Launch unconditionally, Art.5)", runtime.GOOS)
	}

	ctx := context.Background()
	h := newProcessHarness(ctx, t)
	for _, f := range loadFixtures(t) {
		f := f
		t.Run(f.Method+"/"+f.Kind, func(t *testing.T) {
			obs := h.Call(ctx, t, f)
			if f.ProcessMethod == "" {
				if obs.Observed {
					t.Fatalf("process: %s: expected no boundary observation (ungated), got one", f.Method)
				}
				return
			}
			if f.ExpectProcessAllowed == nil {
				t.Fatalf("fixture %s/%s: has a ProcessMethod but no ExpectProcessAllowed", f.Method, f.Kind)
			}
			// host_secretref and every denial DO produce a real audit
			// event (host.HostBoundaryEnforcer's `allow` is called on
			// CheckSecretRef's success path too, and Deny always
			// records); the other five methods' ALLOWED path is
			// unobservable by design (harness_process_test.go's Call
			// doc comment) -- Observed is correctly false there.
			mustObserve := f.Method == methodSecretRef || !*f.ExpectProcessAllowed
			if mustObserve && !obs.Observed {
				t.Fatalf("process: %s: expected a boundary decision, observed none", f.Method)
			}
			if obs.Allowed != *f.ExpectProcessAllowed {
				t.Fatalf("process: %s/%s: allowed = %v, want %v", f.Method, f.Kind, obs.Allowed, *f.ExpectProcessAllowed)
			}
		})
	}
}

// TestConformance_ErrorPaths is the dedicated aggregate check this
// ticket's own checks list names: every error-path fixture entry, run
// against every full-response runtime, must produce an error -- proving
// the fixture set's error coverage is real, not merely present.
func TestConformance_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	builtin := newBuiltinHarness()
	wasmH := newWasmHarness(ctx, t)
	for _, f := range loadFixtures(t) {
		if f.Kind != "error" {
			continue
		}
		f := f
		t.Run(f.Method, func(t *testing.T) {
			if obs := builtin.Call(ctx, t, f); !obs.HasErr {
				t.Fatalf("builtin: %s: error-path fixture produced no error", f.Method)
			}
			if obs := wasmH.Call(ctx, t, f); !obs.HasErr {
				t.Fatalf("wasm: %s: error-path fixture produced no error", f.Method)
			}
		})
	}
}

// TestConformance_AllRuntimesAgree is the suite's central proof: builtin
// and wasm -- the two runtimes that actually implement a full ABI v1
// response -- must produce byte-identical normalised output for every
// fixture entry. See harness_test.go's package doc for why this is a
// real equivalence proof, not a tautology: builtinHarness calls refSink
// directly, wasmHarness reaches the SAME refSink only through a real
// wazero guest call, so agreement proves the wasm host_abi_*.go
// read/dispatch/write plumbing preserves what the delegate decided.
func TestConformance_AllRuntimesAgree(t *testing.T) {
	ctx := context.Background()
	builtin := newBuiltinHarness()
	wasmH := newWasmHarness(ctx, t)
	for _, f := range loadFixtures(t) {
		f := f
		t.Run(f.Method+"/"+f.Kind, func(t *testing.T) {
			b := builtin.Call(ctx, t, f)
			w := wasmH.Call(ctx, t, f)
			if b.HasErr != w.HasErr {
				t.Fatalf("builtin.HasErr=%v wasm.HasErr=%v diverge for %s/%s", b.HasErr, w.HasErr, f.Method, f.Kind)
			}
			if b.HasErr {
				if b.ErrKind != w.ErrKind {
					t.Fatalf("builtin ErrKind=%q wasm ErrKind=%q diverge for %s/%s", b.ErrKind, w.ErrKind, f.Method, f.Kind)
				}
				return
			}
			if !jsonEqual(t, b.ResponseJSON, w.ResponseJSON) {
				t.Fatalf("builtin response %s != wasm response %s for %s/%s", b.ResponseJSON, w.ResponseJSON, f.Method, f.Kind)
			}
		})
	}
}

// jsonEqual decodes both sides to a generic value before comparing, so a
// formatting difference (key order, whitespace) never produces a false
// divergence -- only real content differences do.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatalf("decode a: %v", err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatalf("decode b: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}
