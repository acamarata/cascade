//go:build !windows

// Purpose: composition-root proof for wireCompletionHookPack. It gives
//
//	internal/jobs.NewCompletionPolicy its first real caller (see
//	testonly-allow.json and R-16.83) -- this file dispatches
//	fleet.sessions.completion_check against the REAL registry
//	wireCompletionHookPack builds, over a real sqlite cascade.db under
//	t.TempDir(), proving the wiring end to end: a real Store row decides
//	the outcome, never a mock or a "no panic" smoke test.
//
// SPORT: cmd/cascade/daemon:hooks (TEST) -- P1-E32-W6-S66-T1.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/hookpacks"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_RegistersCompletionGatePack proves the ACTUAL daemon
// composition root (buildRPCServer, daemon_unix_run.go) -- not just
// wireCompletionHookPack called directly -- reaches
// hookpacks.DefaultRegistry. An HTTP-level round trip against
// buildRPCServer's *http.Server cannot prove this: the socket-peer-owner
// check in ConnContext (rpc.ConnContext) 403s any httptest.NewRequest
// call before method dispatch, for every method, registered or not --
// confirmed directly while writing this test, which is why this asserts
// hookpacks.DefaultRegistry's own state (the exact registry
// RegisterCompletionHookPack writes into) rather than a synthetic HTTP
// response. Run in isolation (`-run
// '^TestBuildRPCServer_RegistersCompletionGatePack$'`) this is a true
// mutation proof: this ticket's journal records removing the
// wireCompletionHookPack call from buildRPCServer and re-running this
// single test, which then fails because DefaultRegistry never gains the
// "completion-gate" entry; restoring the call turns it green again.
func TestBuildRPCServer_RegistersCompletionGatePack(t *testing.T) {
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	bus := events.New(storetest.NewMemStore(), clock)
	kvStore := storetest.NewMemStore()
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}

	srv, _, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: paths.SocketPath()}, paths, nil, kvStore)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	if srv == nil {
		t.Fatal("buildRPCServer returned a nil *http.Server")
	}

	found := false
	for _, pack := range hookpacks.DefaultRegistry.Packs() {
		if pack.Name != "completion-gate" {
			continue
		}
		hasTaskCompleted, hasStop := false, false
		for _, d := range pack.Descriptors {
			if d.EventType == hookpacks.EventTaskCompleted {
				hasTaskCompleted = true
			}
			if d.EventType == hookpacks.EventStop {
				hasStop = true
			}
		}
		found = hasTaskCompleted && hasStop
	}
	if !found {
		t.Fatal("buildRPCServer's own composition did not register the completion-gate hook pack (TaskCompleted+Stop) on hookpacks.DefaultRegistry")
	}
}

// setupCompletionHooks wires the real completion-gate composition over a
// fresh cascade.db under t.TempDir(), returning the registry to dispatch
// against and the *jobs.Store this test seeds fixtures through directly
// (the same db handle wireCompletionHookPack opened internally).
func setupCompletionHooks(t *testing.T) (*rpc.Registry, *jobs.Store) {
	t.Helper()
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))
	registry := rpc.NewRegistry()
	kv := storetest.NewMemStore()
	bus := events.New(kv, clock)

	if err := wireCompletionHookPack(context.Background(), registry, kv, clock, bus, paths); err != nil {
		t.Fatalf("wireCompletionHookPack: %v", err)
	}

	dbPath := paths.DataDir() + "/cascade.db"
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return registry, jobs.NewStore(db)
}

func dispatchCompletionCheck(t *testing.T, registry *rpc.Registry, jobID string) hookpacks.CompletionHookResponse {
	t.Helper()
	raw, err := json.Marshal(hookpacks.CompletionHookPayload{JobID: jobID, EventType: hookpacks.EventStop})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	got, errObj := registry.Dispatch(context.Background(), &rpc.Request{Method: hookpacks.MethodCompletionCheck, Params: raw})
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	resp, ok := got.(hookpacks.CompletionHookResponse)
	if !ok {
		t.Fatalf("Dispatch returned %#v, want hookpacks.CompletionHookResponse", got)
	}
	return resp
}

// TestWireCompletionHookPack_MountsCompletionCheck proves
// fleet.sessions.completion_check is registered on the real daemon
// composition root -- the mutation this ticket's journal records removes
// the call in daemon_unix_run.go and shows this test fail with
// "method not found".
func TestWireCompletionHookPack_MountsCompletionCheck(t *testing.T) {
	registry, _ := setupCompletionHooks(t)
	if !registry.Registered(hookpacks.MethodCompletionCheck) {
		t.Fatalf("registry.Registered(%q) = false, want true", hookpacks.MethodCompletionCheck)
	}
}

// TestWireCompletionHookPack_UnseededJobDeniesUnknown proves
// completionJobResolver reaches the REAL *jobs.Store: an id no row backs
// is denied by name, not silently allowed.
func TestWireCompletionHookPack_UnseededJobDeniesUnknown(t *testing.T) {
	registry, _ := setupCompletionHooks(t)
	resp := dispatchCompletionCheck(t, registry, "ghost-job")
	if !resp.Deny {
		t.Fatalf("completion check for an unseeded job = %+v, want Deny=true", resp)
	}
}

// TestWireCompletionHookPack_RunningJobWithNoEvidenceDenies proves
// completionPolicyGate reaches the REAL *jobs.CompletionPolicy.Transition:
// a running job with zero evidence rows fails the risk-class gate set and
// is denied with a real "missing evidence" reason, never a placeholder.
func TestWireCompletionHookPack_RunningJobWithNoEvidenceDenies(t *testing.T) {
	registry, store := setupCompletionHooks(t)
	seedRunningJob(t, store, "job-no-evidence")

	resp := dispatchCompletionCheck(t, registry, "job-no-evidence")
	if !resp.Deny || resp.Reason == "" {
		t.Fatalf("completion check on a job with no evidence = %+v, want a real Deny reason", resp)
	}
	if got := resp.Reason; !strings.Contains(got, "missing evidence") {
		t.Fatalf("deny reason = %q, want it to name missing evidence", got)
	}

	// Store-state proof (AGENT-BRIEF: assert a row, not just an event): the
	// job must still be Running -- a real denial never advances state.
	j, found, err := store.GetJob(context.Background(), "job-no-evidence")
	if err != nil || !found {
		t.Fatalf("GetJob after deny: found=%v err=%v", found, err)
	}
	if j.State != jobs.JobStateRunning {
		t.Fatalf("job State after a denied completion check = %q, want %q (unchanged)", j.State, jobs.JobStateRunning)
	}
}

func seedRunningJob(t *testing.T, store *jobs.Store, id string) {
	t.Helper()
	err := store.PutJob(context.Background(), jobs.Job{
		ID: id, State: jobs.JobStateRunning, CreatedAt: 1, UpdatedAt: 1,
		RiskClass: "normal", ConsequenceClass: jobs.ConsequenceNormal, DataClass: jobs.DataClassInternal,
	})
	if err != nil {
		t.Fatalf("PutJob(%s): %v", id, err)
	}
}
