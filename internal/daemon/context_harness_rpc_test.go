package daemon

// Purpose: covers RegisterContextHarnessHandlers and
//   ComputeContextHarnessList (context_harness.go), P1-E16-W4-S35-T3's
//   daemon side, through the REAL rpc.Registry.Dispatch entry point.
// Constraints: Art.7.1 -- HOME and the cwd are both temp directories, so
//   the detector reads a tree this test created rather than whatever
//   harnesses the machine running it happens to have installed.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"testing"

	cascadecontext "github.com/acamarata/cascade/internal/context"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// harnessRPCRegistry registers both methods against a pinned HOME.
func harnessRPCRegistry(t *testing.T, home string) *rpc.Registry {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	registry := rpc.NewRegistry()
	if err := RegisterContextHarnessHandlers(registry, fakePathsFor(t, ""), runtime.NewSystemClock()); err != nil {
		t.Fatalf("RegisterContextHarnessHandlers: %v", err)
	}
	return registry
}

// TestContextHarnessListReportsEverySupportedHarness drives the method
// over a HOME with exactly one harness installed. Both halves are the
// point: the installed one must come back Detected, and the absent ones
// must still appear. A list that omitted what is missing could not tell
// "not installed" from "this build does not know about it", which is the
// question an operator runs this to answer.
func TestContextHarnessListReportsEverySupportedHarness(t *testing.T) {
	home := t.TempDir()
	installed := filepath.Join(home, ".claude")
	if err := os.MkdirAll(installed, 0o750); err != nil {
		t.Fatalf("seeding a harness directory: %v", err)
	}
	registry := harnessRPCRegistry(t, home)

	raw, err := json.Marshal(ContextHarnessListParams{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextHarnessListMethod, Params: raw, ID: json.RawMessage(`1`),
	})
	if goruntime.GOOS == "windows" {
		assertTier2Refusal(t, errObj)
		return
	}
	if errObj != nil {
		t.Fatalf("Dispatch(%s): %+v", ContextHarnessListMethod, errObj)
	}
	got, ok := result.(ContextHarnessListResult)
	if !ok {
		t.Fatalf("result type = %T, want ContextHarnessListResult", result)
	}

	assertEveryHarnessReported(t, got.Harnesses, installed)
}

// TestContextHarnessListRefusesAnEmptyCwd: the daemon's own working
// directory is meaningless to a client asking about a project, so
// defaulting to it would answer a different question than the one asked.
func TestContextHarnessListRefusesAnEmptyCwd(t *testing.T) {
	registry := harnessRPCRegistry(t, t.TempDir())

	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextHarnessListMethod,
		Params: json.RawMessage(`{}`), ID: json.RawMessage(`1`),
	})
	if errObj == nil {
		t.Fatal("Dispatch accepted a list request with no cwd")
	}
	if errObj.Code != cascade.KindInvalidInput.JSONRPCCode() {
		t.Errorf("error code = %d, want the KindInvalidInput code %d",
			errObj.Code, cascade.KindInvalidInput.JSONRPCCode())
	}
}

// TestContextHarnessListRefusesUndecodableParams keeps a garbled request
// from being read as "no cwd supplied" and then as a default.
func TestContextHarnessListRefusesUndecodableParams(t *testing.T) {
	registry := harnessRPCRegistry(t, t.TempDir())

	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextHarnessListMethod,
		Params: json.RawMessage(`["/tmp"]`), ID: json.RawMessage(`1`),
	})
	if errObj == nil {
		t.Fatal("Dispatch accepted params it could not decode")
	}
}

// TestContextHarnessSyncIsTheSameComputationAsContextSync is the
// no-second-implementation assertion at the RPC layer: harness_sync is a
// second NAME for context.sync, so over the same empty tree both methods
// must return the same result type and the same verdict.
func TestContextHarnessSyncIsTheSameComputationAsContextSync(t *testing.T) {
	home := t.TempDir()
	registry := harnessRPCRegistry(t, home)
	if err := RegisterContextSyncHandler(registry, fakePathsFor(t, ""), runtime.NewSystemClock()); err != nil {
		t.Fatalf("RegisterContextSyncHandler: %v", err)
	}
	cwd := t.TempDir()

	results := map[string]ContextSyncResult{}
	for _, method := range []string{ContextHarnessSyncMethod, ContextSyncMethod} {
		raw, err := json.Marshal(ContextSyncParams{Cwd: cwd, CheckOnly: true})
		if err != nil {
			t.Fatalf("marshal params: %v", err)
		}
		result, errObj := registry.Dispatch(context.Background(), &rpc.Request{
			JSONRPC: "2.0", Method: method, Params: raw, ID: json.RawMessage(`1`),
		})
		if errObj != nil {
			t.Fatalf("Dispatch(%s): %+v", method, errObj)
		}
		typed, ok := result.(ContextSyncResult)
		if !ok {
			t.Fatalf("%s result type = %T, want ContextSyncResult", method, result)
		}
		results[method] = typed
	}
	if len(results[ContextHarnessSyncMethod].Drift) != len(results[ContextSyncMethod].Drift) {
		t.Errorf("the two spellings disagree: harness_sync reported %d drift entries, sync reported %d",
			len(results[ContextHarnessSyncMethod].Drift), len(results[ContextSyncMethod].Drift))
	}
}

// TestContextHarnessSyncRefusesAnEmptyCwd covers the shared param decoder
// reached through the harness spelling.
func TestContextHarnessSyncRefusesAnEmptyCwd(t *testing.T) {
	registry := harnessRPCRegistry(t, t.TempDir())

	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{
		JSONRPC: "2.0", Method: ContextHarnessSyncMethod,
		Params: json.RawMessage(`{}`), ID: json.RawMessage(`1`),
	})
	if errObj == nil {
		t.Fatal("Dispatch accepted a harness sync with no cwd")
	}
}

// assertTier2Refusal requires the method to surface the detector's own
// refusal on a platform whose harness paths this build does not resolve.
//
// An empty list would read as "no harnesses installed" on a machine
// nobody could look at, which is the false negative this whole surface
// exists to prevent. Asserted rather than skipped: the windows lane is
// the only runner that takes this branch.
func assertTier2Refusal(t *testing.T, errObj *rpc.ErrorObject) {
	t.Helper()
	if errObj == nil {
		t.Fatal("the method answered on a platform where detection is not available")
	}
	if errObj.Code != cascade.KindUnsupported.JSONRPCCode() {
		t.Errorf("error code = %d, want the KindUnsupported code %d",
			errObj.Code, cascade.KindUnsupported.JSONRPCCode())
	}
}

// assertEveryHarnessReported requires a row for each supported harness,
// each naming where it was probed, with the seeded one detected and the
// others not.
//
// Both directions matter. A test that only checked the installed harness
// would pass against a detector that reported everything installed, which
// is the more dangerous of the two wrong answers here.
func assertEveryHarnessReported(t *testing.T, rows []cascadecontext.HarnessState, seededPath string) {
	t.Helper()
	seen := map[cascadecontext.HarnessKind]cascadecontext.HarnessState{}
	for _, state := range rows {
		seen[state.Kind] = state
	}
	for _, kind := range cascadecontext.SupportedHarnesses() {
		state, present := seen[kind]
		if !present {
			t.Errorf("%s has no row at all; an absent harness must be reported, not dropped", kind)
			continue
		}
		if state.InstallPath == "" {
			t.Errorf("%s reports no path; a false negative is undebuggable without one", kind)
		}
	}
	if !seen[cascadecontext.HarnessClaude].Detected {
		t.Errorf("the seeded harness at %s was not detected", seededPath)
	}
	if seen[cascadecontext.HarnessCodex].Detected {
		t.Error("a harness that is not installed was reported as detected")
	}
}
