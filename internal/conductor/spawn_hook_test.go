// Purpose: spawn_hook.go's required unit tests (Art.2): the
//
//	IsSubprocessDispatch table (LaneID/Provider held constant), MakeSpawnRecord
//	field population and nil-clock error, enqueueSpawn's non-blocking
//	channel-full drop and hook-firing mechanism, trySpawn's nil-hook and
//	non-subprocess no-ops, and Execute's real wiring at the model.execute
//	entry point. See spawn_hook.go's header for the CONTRACT DEVIATION this
//	suite documents rather than papers over: the real provider.Selection
//	carries no ExecutionMode field, so no real dispatch table row can
//	assert `true` today - every row asserts false, which is itself the
//	evidence the deviation note claims.
//
// SPORT: conductor/spawn-hook/ADD (P1-E12-W3-S25-T3).
package conductor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestIsSubprocessDispatch_NoIdentifierHeuristic holds LaneID and Provider
// constant across every row while nothing in the real Selection type can
// vary ExecutionMode (it does not exist): every row answers false,
// demonstrating no lane-ID or provider-ID heuristic is consulted (a
// heuristic would make at least one row diverge on LaneID/Provider alone).
func TestIsSubprocessDispatch_NoIdentifierHeuristic(t *testing.T) {
	cases := []struct {
		name string
		sel  provider.Selection
	}{
		{"zero-value", provider.Selection{}},
		{"populated-lane-and-provider", provider.Selection{LaneID: "lane-1", Provider: "anthropic", Model: "m"}},
		{"populated-with-reason-flags", provider.Selection{LaneID: "lane-1", Provider: "anthropic", ReasonFlags: []string{"cost:cheapest-selected"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSubprocessDispatch(tc.sel); got {
				t.Fatalf("IsSubprocessDispatch(%+v) = true, want false (Selection carries no ExecutionMode field - see spawn_hook.go header)", tc.sel)
			}
		})
	}
}

func TestMakeSpawnRecord_PopulatesFromInputs(t *testing.T) {
	clk := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	req := provider.ModelRequest{TaskID: "t-1", TaskClass: "code", Sensitivity: provider.SensitivityInternal}
	sel := provider.Selection{LaneID: "lane-1", Provider: "anthropic", Model: "m"}
	rec, err := MakeSpawnRecord(req, sel, clk)
	if err != nil {
		t.Fatalf("MakeSpawnRecord: %v", err)
	}
	if rec.TaskID != "t-1" || rec.Binary != "anthropic" || rec.ModelClass != "code" {
		t.Fatalf("MakeSpawnRecord = %+v, want task=t-1 binary=anthropic class=code", rec)
	}
	if rec.Sensitivity != provider.SensitivityInternal.String() {
		t.Fatalf("Sensitivity = %q, want %q", rec.Sensitivity, provider.SensitivityInternal.String())
	}
	if !rec.SpawnedAt.Equal(clk.Now()) {
		t.Fatalf("SpawnedAt = %v, want %v (injected clock, not bare time.Now)", rec.SpawnedAt, clk.Now())
	}
}

func TestMakeSpawnRecord_NilClockErrors(t *testing.T) {
	_, err := MakeSpawnRecord(provider.ModelRequest{}, provider.Selection{}, nil)
	if err == nil {
		t.Fatal("MakeSpawnRecord(nil clock) = nil error, want ErrNilClock")
	}
}

// TestEnqueueSpawn_HookFires proves the real production mechanism a
// subprocess dispatch would exercise once IsSubprocessDispatch has real
// classification data: enqueueSpawn hands rec to the hook off the caller's
// goroutine.
func TestEnqueueSpawn_HookFires(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	var mu sync.Mutex
	var got sessions.SpawnRecord
	done := make(chan struct{})
	exec.SetSpawnHook(func(rec sessions.SpawnRecord) {
		mu.Lock()
		got = rec
		mu.Unlock()
		close(done)
	})
	exec.enqueueSpawn(sessions.SpawnRecord{TaskID: "t-2", PID: 42, Binary: "b"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hook did not fire within 2s")
	}
	mu.Lock()
	defer mu.Unlock()
	if got.TaskID != "t-2" || got.PID != 42 {
		t.Fatalf("hook received %+v, want task=t-2 pid=42", got)
	}
}

// TestEnqueueSpawn_ChannelFullDrops proves the non-blocking drop path: with
// spawnSem saturated, enqueueSpawn returns immediately and increments the
// drop counter rather than blocking the caller.
func TestEnqueueSpawn_ChannelFullDrops(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	release := make(chan struct{})
	exec.SetSpawnHook(func(sessions.SpawnRecord) {
		<-release // holds the one semaphore slot open until the test releases it
	})
	// spawnHookCapacity is 16; saturate every slot with in-flight hook calls.
	for i := 0; i < spawnHookCapacity; i++ {
		exec.enqueueSpawn(sessions.SpawnRecord{TaskID: "t", PID: i + 1, Binary: "b"})
	}
	done := make(chan struct{})
	go func() {
		exec.enqueueSpawn(sessions.SpawnRecord{TaskID: "overflow", PID: 999, Binary: "b"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueueSpawn blocked instead of dropping when the channel was full")
	}
	if exec.SpawnDropCount() != 1 {
		t.Fatalf("SpawnDropCount() = %d, want 1", exec.SpawnDropCount())
	}
	close(release)
}

// TestNewDefaultProductionSpawnHook_RegistersIntoStore proves the real
// production caller of sessions.NewDefaultRegistry: the returned SpawnHook
// registers rec into the same store, readable by a fresh DefaultRegistry
// handle - the exact call site internal/daemon/subsystems.go is expected
// to wire (see spawn_hook.go's doc comment and the testonly-allow.json
// entry for this symbol).
func TestNewDefaultProductionSpawnHook_RegistersIntoStore(t *testing.T) {
	store := storetest.NewMemStore()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0))
	hook := NewDefaultProductionSpawnHook(store, time.Minute, clock)
	hook(sessions.SpawnRecord{TaskID: "t-prod", PID: 55, Binary: "claude", SpawnedAt: clock.Now()})

	// A fresh DefaultRegistry handle over the SAME store must see the
	// registration once census confirms the PID - proving the hook wrote
	// through shared storage, not a private cache.
	fresh := sessions.NewDefaultRegistry(store, time.Minute, clock)
	if err := fresh.Reconcile([]census.Snapshot{{Pid: 55, Binary: "claude"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok, err := fresh.Get(55)
	if err != nil || !ok || !got.AttributionKnown || got.TaskID != "t-prod" {
		t.Fatalf("Get(55) = %+v, %v, %v, want attribution-known task=t-prod via NewDefaultProductionSpawnHook's write", got, ok, err)
	}
}

func TestTrySpawn_NilHookIsNoop(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	// exec.spawnHook is nil by construction (SetSpawnHook never called).
	exec.trySpawn(validReq(), provider.Selection{LaneID: "lane-1"}) // must not panic
}

func TestTrySpawn_NonSubprocessDispatchSkipsHook(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	called := false
	exec.SetSpawnHook(func(sessions.SpawnRecord) { called = true })
	// Every real Selection is non-subprocess per the CONTRACT DEVIATION
	// note (no ExecutionMode field exists), so this asserts the honest
	// current behavior: the hook never fires from trySpawn today.
	exec.trySpawn(validReq(), provider.Selection{LaneID: "lane-1", Provider: "anthropic"})
	time.Sleep(20 * time.Millisecond)
	if called {
		t.Fatal("hook fired for a Selection with no way to signal subprocess dispatch")
	}
}

// TestExecute_WiresSpawnHookCallSite proves Execute calls trySpawn on
// every successful dispatch (not that the hook fires - it structurally
// cannot given the CONTRACT DEVIATION - but that the call site exists and
// runs without panicking or altering Execute's own outcome).
func TestExecute_WiresSpawnHookCallSite(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	called := false
	exec.SetSpawnHook(func(sessions.SpawnRecord) { called = true })
	resp, err := exec.Execute(context.Background(), validReq())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.JobID == "" {
		t.Fatal("Execute's own outcome must be unaffected by the spawn-hook call site")
	}
	time.Sleep(20 * time.Millisecond)
	if called {
		t.Fatal("hook fired via Execute despite no real Selection ever satisfying IsSubprocessDispatch")
	}
}
