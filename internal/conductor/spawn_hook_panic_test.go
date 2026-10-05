// Purpose: enqueueSpawn's panic isolation. A spawn hook runs on its own
//
//	goroutine, so a panic in it would otherwise end the whole process; the
//	recover in enqueueSpawn must keep the process alive, free the semaphore
//	slot and count the panic.
//
// SPORT: conductor/spawn-hook (CHANGED, panic recovery).
package conductor

import (
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
)

// TestEnqueueSpawnRecoversPanic enqueues one panicking hook and then claims
// every semaphore slot itself. The claims can only all succeed once the
// panicking goroutine has released its slot, and the release runs after the
// recover (deferred calls run last-in first-out), so the counter read that
// follows is ordered after the increment by the channel operations alone.
func TestEnqueueSpawnRecoversPanic(t *testing.T) {
	exec, _ := newReadyExecutor(t)
	entered := make(chan struct{})
	exec.SetSpawnHook(func(sessions.SpawnRecord) {
		close(entered)
		panic("spawn hook boom")
	})
	before := spawnHookPanics.Load()
	exec.enqueueSpawn(sessions.SpawnRecord{TaskID: "t-panic", PID: 7, Binary: "b"})

	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	select {
	case <-entered:
	case <-guard.C:
		t.Fatal("the spawn hook never ran")
	}

	claimed := make(chan struct{})
	go func() {
		defer close(claimed)
		for i := 0; i < spawnHookCapacity; i++ {
			exec.spawnSem <- struct{}{}
		}
	}()
	select {
	case <-claimed:
	case <-guard.C:
		t.Fatal("the panicking hook never freed its semaphore slot")
	}
	for i := 0; i < spawnHookCapacity; i++ {
		<-exec.spawnSem
	}

	if got := spawnHookPanics.Load() - before; got != 1 {
		t.Fatalf("spawnHookPanics grew by %d, want 1", got)
	}
	if exec.SpawnDropCount() != 0 {
		t.Fatalf("SpawnDropCount() = %d, want 0 (the panic is not a drop)", exec.SpawnDropCount())
	}
}
