//go:build !windows

// Purpose: the mutation-provable proof that wireConductorExecute (this
// composition root's R-16.80 call site) really constructs and wires
// internal/providers/dispatch.NewResolver, closing DEFECT-conductor-
// execute-permanently-unavailable.md: before this fix the resolver
// argument here was a literal nil, and "conductor.execute" answered
// ErrConstructionFailed for every call, forever. This test dials the
// method for real through registry.Dispatch, never the closures directly.
// SPORT: cmd/cascade/daemon (CHANGE, DEFECT-conductor-execute-
// permanently-unavailable.md).
package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestWireConductorExecute_ConstructsARealExecutor is the acceptance-
// critical proof: with today's real wireConductorExecute, dialing
// "conductor.execute" no longer answers the permanent, construction-time
// "executor unavailable" refusal a nil resolver argument produces
// (internal/daemon's own TestRegisterConductorExecuteHandler_NilResolver_
// RealRefusal names that exact string), and no longer stops at the
// security-pipeline gate either.
//
// UPDATED by P1-E10-W4-S87-T1: this test used to REQUIRE
// ErrSecurityPipelineNotReady, because Classifier/Taxonomy/Policy/
// Sensitivity/Firewall were unwired here. The W3 hardening gate found what
// that meant in the product — a real `cascade run` refused at cascade's own
// door — so they are wired now, and the assertion moved forward one stage:
// the request must get PAST readiness and be judged on its own merits. A
// bare task_class with no inputs is an invalid request, which is the real
// Executor's own validation speaking.
func TestWireConductorExecute_ConstructsARealExecutor(t *testing.T) {
	dir := t.TempDir()
	paths := fakeMemoryPaths{root: dir}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := runtime.NewSystemClock()
	store := storetest.NewMemStore()
	registry := rpc.NewRegistry()
	manifest := daemon.NewManifest(nil, clock)

	if err := wireConductorExecute(context.Background(), registry, manifest, paths, clock, store, nil); err != nil {
		t.Fatalf("wireConductorExecute: %v", err)
	}
	if !registry.Registered(daemon.ConductorExecuteMethod) {
		t.Fatal("conductor.execute was never registered")
	}

	params, err := json.Marshal(runRequestParams{TaskID: "t1", TaskClass: "chat"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	_, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: daemon.ConductorExecuteMethod, Params: params})
	if errObj == nil {
		t.Fatal("Dispatch() = nil error, want the real Executor's own validation refusal")
	}
	if strings.Contains(errObj.Message, "executor unavailable") {
		t.Fatalf("error message = %q: still the nil-resolver permanent-refusal branch", errObj.Message)
	}
	if strings.Contains(errObj.Message, conductor.ErrSecurityPipelineNotReady.Error()) {
		t.Fatalf("error message = %q: the security pipeline is still unwired at this composition root", errObj.Message)
	}
	if !strings.Contains(errObj.Message, conductor.ErrInvalidRequest.Error()) {
		t.Fatalf("error message = %q, want the real Executor's own ErrInvalidRequest", errObj.Message)
	}

	if !registry.Registered(conductor.JobCancelMethod) {
		t.Fatal("job.cancel was not registered alongside the real executor")
	}
}
