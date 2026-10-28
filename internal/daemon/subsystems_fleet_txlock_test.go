package daemon

// Purpose: the runtime's cascade.db handle takes the write lock at BEGIN
//
//	(_txlock=immediate) and waits under busy_timeout, so a lease
//	transaction never fails SQLITE_BUSY_SNAPSHOT against another
//	connection's committed writes (P1-BF-R143).
//
// SPORT: internal/daemon (ADD, P1-CAP-10).

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/providers/sqlite"
)

// TestFleetRuntimeDBBeginsImmediate: with a providers/sqlite Driver on the
// same cascade.db committing in a loop, 200 Acquire/Release cycles on the
// runtime LeaseManager return no error; and a read-then-write transaction
// with a foreign commit between its read and its write still commits.
func TestFleetRuntimeDBBeginsImmediate(t *testing.T) {
	g := newResRig(t)
	drv, err := sqlite.Open(context.Background(), filepath.Join(g.deps.Paths.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() { _ = drv.Close() })
	stop, wg := make(chan struct{}), sync.WaitGroup{}
	var writeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := drv.Put(context.Background(), "txlock", fmt.Sprintf("k%d", i%16), []byte("v")); err != nil {
				writeErr = err
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		got, err := g.rt.Leases().Acquire(g.ctx, "repo-tx", "t/**", "job-tx")
		if err == nil {
			err = g.rt.Leases().Release(g.ctx, "repo-tx", got.Lease.ScopeGlob, got.Lease.Epoch)
		}
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
	if writeErr != nil {
		t.Fatalf("driver writes: %v", writeErr)
	}
	assertInterleaveCommits(t, g, drv)
}

// assertInterleaveCommits reads in a runtime transaction, lets the driver
// commit, then writes in the same transaction: IMMEDIATE holds the lock
// from BEGIN, so the driver waits and the write commits.
func assertInterleaveCommits(t *testing.T, g *resRig, drv *sqlite.Driver) {
	t.Helper()
	tx, err := g.rt.JobsDB().BeginTx(g.ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM jobs_resource_lease`).Scan(&n); err != nil {
		t.Fatalf("read: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- drv.Put(context.Background(), "txlock", "between", []byte("v")) }()
	select {
	case err := <-done: // DEFERRED: the foreign write lands between the read and the write
		done <- err
	case <-time.After(200 * time.Millisecond): // IMMEDIATE: the driver waits on busy_timeout
	}
	if _, err := tx.Exec(`UPDATE jobs_resource_lease SET renew_count = renew_count + 0`); err != nil {
		t.Fatalf("write after a foreign commit: %v (the handle does not begin IMMEDIATE)", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("driver write after the transaction: %v", err)
	}
}
