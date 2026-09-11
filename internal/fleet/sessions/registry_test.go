// Purpose: DefaultRegistry's required unit tests (Art.2): Register+
//
//	Reconcile happy path, PID dedup, census-only pass-through, pending
//	eviction under an injected frozen clock, empty-snapshot no-op,
//	Register's fail-closed input validation, concurrent Register+
//	Reconcile under -race, and the fresh-handle unification proof (two
//	independently-constructed DefaultRegistry values sharing one store).
//
// SPORT: fleet/sessions-registry/ADD (P1-E12-W3-S25-T3).
package sessions_test

import (
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

func newRegistry(t *testing.T, timeout time.Duration) (*sessions.DefaultRegistry, *storetest.MemStore, *testkit.FrozenClock) {
	t.Helper()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	return sessions.NewDefaultRegistry(store, timeout, clock), store, clock
}

func TestRegistry_RegisterReconcileHappyPath(t *testing.T) {
	reg, _, clock := newRegistry(t, time.Minute)
	rec := sessions.SpawnRecord{TaskID: "t1", PID: 100, Binary: "claude", Account: "a1", ModelClass: "code", Sensitivity: "internal", SpawnedAt: clock.Now()}
	if err := reg.Register(rec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Reconcile([]census.Snapshot{{Pid: 100, Binary: "claude", Account: "a1"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok, err := reg.Get(100)
	if err != nil || !ok {
		t.Fatalf("Get(100) = %+v, %v, %v", got, ok, err)
	}
	if !got.AttributionKnown || got.TaskID != "t1" || got.ModelClass != "code" {
		t.Fatalf("Get(100) = %+v, want attribution-known t1/code", got)
	}
}

func TestRegistry_PIDDedup_SpawnRecordWinsOverCensus(t *testing.T) {
	reg, _, clock := newRegistry(t, time.Minute)
	rec := sessions.SpawnRecord{TaskID: "t9", PID: 200, Binary: "codex", Account: "spawned-acct", ModelClass: "review", Sensitivity: "restricted", SpawnedAt: clock.Now()}
	if err := reg.Register(rec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// census reports a different account for the same PID: SpawnRecord
	// fields must win on PID match.
	if err := reg.Reconcile([]census.Snapshot{{Pid: 200, Binary: "codex", Account: "census-acct", Flags: []string{"--foo"}}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok, err := reg.Get(200)
	if err != nil || !ok {
		t.Fatalf("Get(200) = %+v, %v, %v", got, ok, err)
	}
	if got.Account != "spawned-acct" || got.TaskID != "t9" {
		t.Fatalf("Get(200) = %+v, want SpawnRecord fields to win (account=spawned-acct task=t9)", got)
	}
	if len(got.Flags) != 1 || got.Flags[0] != "--foo" {
		t.Fatalf("Get(200).Flags = %v, want census-supplied flags to supplement", got.Flags)
	}
}

func TestRegistry_CensusOnlyPassThrough_NotDropped(t *testing.T) {
	reg, _, _ := newRegistry(t, time.Minute)
	if err := reg.Reconcile([]census.Snapshot{{Pid: 300, Binary: "opencode", Account: "a2"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok, err := reg.Get(300)
	if err != nil || !ok {
		t.Fatalf("Get(300) = %+v, %v, %v, want a census-only pass-through session present", got, ok, err)
	}
	if got.AttributionKnown || got.TaskID != "" || got.ModelClass != "" {
		t.Fatalf("Get(300) = %+v, want attribution-unknown (empty TaskID/ModelClass)", got)
	}
}

func TestRegistry_PendingEviction_UnderFrozenClock(t *testing.T) {
	reg, _, clock := newRegistry(t, time.Minute)
	rec := sessions.SpawnRecord{TaskID: "t5", PID: 400, Binary: "claude", SpawnedAt: clock.Now()}
	if err := reg.Register(rec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// PID 400 never appears in any snapshot. Advance past the timeout and
	// reconcile an unrelated PID to trigger eviction bookkeeping.
	clock.Advance(2 * time.Minute)
	if err := reg.Reconcile([]census.Snapshot{{Pid: 999, Binary: "other"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok, err := reg.Get(400); err != nil {
		t.Fatalf("Get(400): %v", err)
	} else if ok {
		t.Fatalf("Get(400) found a unified session, want the never-confirmed pending registration evicted")
	}
	// A later census confirmation must not resurrect the evicted entry as
	// attributed: it comes back as a fresh census-only session.
	if err := reg.Reconcile([]census.Snapshot{{Pid: 400, Binary: "claude"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, ok, err := reg.Get(400)
	if err != nil || !ok {
		t.Fatalf("Get(400) after re-discovery = %+v, %v, %v", got, ok, err)
	}
	if got.AttributionKnown {
		t.Fatalf("Get(400) = %+v, want attribution-unknown after eviction, never surfaced as active attribution", got)
	}
}

func TestRegistry_ReconcileEmptySnapshot_NoOpOnExisting(t *testing.T) {
	reg, _, clock := newRegistry(t, time.Minute)
	rec := sessions.SpawnRecord{TaskID: "t7", PID: 500, Binary: "claude", SpawnedAt: clock.Now()}
	if err := reg.Register(rec); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Reconcile([]census.Snapshot{{Pid: 500, Binary: "claude"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// An empty snapshot slice must not clear the already-unified entry.
	if err := reg.Reconcile(nil); err != nil {
		t.Fatalf("Reconcile(nil): %v", err)
	}
	got, ok, err := reg.Get(500)
	if err != nil || !ok || !got.AttributionKnown {
		t.Fatalf("Get(500) after empty Reconcile = %+v, %v, %v, want the existing unified entry unchanged", got, ok, err)
	}
}

func TestRegistry_Register_InvalidInputs(t *testing.T) {
	reg, _, clock := newRegistry(t, time.Minute)
	cases := []struct {
		name string
		rec  sessions.SpawnRecord
	}{
		{"pid<=0", sessions.SpawnRecord{TaskID: "t", PID: 0, Binary: "b", SpawnedAt: clock.Now()}},
		{"pid-negative", sessions.SpawnRecord{TaskID: "t", PID: -1, Binary: "b", SpawnedAt: clock.Now()}},
		{"empty-taskid", sessions.SpawnRecord{TaskID: "", PID: 1, Binary: "b", SpawnedAt: clock.Now()}},
		{"empty-binary", sessions.SpawnRecord{TaskID: "t", PID: 1, Binary: "", SpawnedAt: clock.Now()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := reg.Register(tc.rec); err == nil {
				t.Fatalf("Register(%+v) = nil, want a non-nil error", tc.rec)
			}
		})
	}
}

func TestRegistry_ConcurrentRegisterReconcile(t *testing.T) {
	reg, _, clock := newRegistry(t, time.Minute)
	var wg sync.WaitGroup
	for i := 1; i <= 20; i++ {
		wg.Add(2)
		pid := i
		go func() {
			defer wg.Done()
			_ = reg.Register(sessions.SpawnRecord{TaskID: "t", PID: pid, Binary: "b", SpawnedAt: clock.Now()})
		}()
		go func() {
			defer wg.Done()
			_ = reg.Reconcile([]census.Snapshot{{Pid: pid, Binary: "b"}})
		}()
	}
	wg.Wait()
	if _, err := reg.List(); err != nil {
		t.Fatalf("List after concurrent access: %v", err)
	}
}

// TestRegistry_FreshHandleUnification proves the registry is genuinely
// unified through shared storage, not through one instance's in-memory
// cache: Register happens on regA, Reconcile happens on regB, and a THIRD
// instance (regC) - constructed after both writes, sharing the same
// backing store - reads the merged result. This is the sibling-registry
// trap named in the ticket: `provider add` writing one registry while
// `list` read a different one.
func TestRegistry_FreshHandleUnification(t *testing.T) {
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	regA := sessions.NewDefaultRegistry(store, time.Minute, clock)
	regB := sessions.NewDefaultRegistry(store, time.Minute, clock)
	regC := sessions.NewDefaultRegistry(store, time.Minute, clock)

	if err := regA.Register(sessions.SpawnRecord{TaskID: "t-fresh", PID: 700, Binary: "claude", ModelClass: "code", SpawnedAt: clock.Now()}); err != nil {
		t.Fatalf("regA.Register: %v", err)
	}
	if err := regB.Reconcile([]census.Snapshot{{Pid: 700, Binary: "claude"}}); err != nil {
		t.Fatalf("regB.Reconcile: %v", err)
	}
	got, ok, err := regC.Get(700)
	if err != nil || !ok {
		t.Fatalf("regC.Get(700) = %+v, %v, %v, want the write from regA merged by regB visible to a fresh handle", got, ok, err)
	}
	if !got.AttributionKnown || got.TaskID != "t-fresh" {
		t.Fatalf("regC.Get(700) = %+v, want attribution-known task=t-fresh via shared storage, not the writer's own cache", got)
	}
}
