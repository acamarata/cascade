// Purpose: the lease epoch is presented again around every sub-job: a lease
// reclaimed while a sub-job runs records no stream marker, publishes no
// terminal event and fires no OnTerminal; a lease reclaimed while the
// dispatcher was down runs nothing on Resume, which drops the rows.
//
// SPORT: internal.ci.Dispatcher.Dispatch/TESTED (P1-CI-01).
package ci

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// reclaimLease moves the rig's lease to a newer epoch, as a reclaim does.
func (r *streamRig) reclaimLease() {
	r.t.Helper()
	l := r.lease
	l.Epoch++
	if err := r.store.PutLease(context.Background(), l); err != nil {
		r.t.Fatalf("PutLease: %v", err)
	}
}

func TestLeaseReclaimedDuringSubJobRecordsNothing(t *testing.T) {
	r := newRig(t)
	r.commit(map[string]string{"docs/a.md": "a"})
	var fired int
	r.d.OnTerminal(func(CIResultEvent) { fired++ })
	var started sync.WaitGroup
	var once sync.Once
	started.Add(2)
	r.exec.fn = func(SubJob) (SubJobResult, error) {
		started.Done()
		started.Wait() // both kinds are past their pre-run fence
		once.Do(r.reclaimLease)
		return SubJobResult{RunID: -int64(r.exec.count()), RepoID: 7, Passed: true, ExecutorKind: "fake"}, nil
	}
	_, err := r.checkpoint()
	assertFenced(t, err)
	if r.exec.count() != 2 {
		t.Fatalf("sub-jobs run = %d, want 2 (the reclaim happens during the run)", r.exec.count())
	}
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_run_stream`); n != 0 {
		t.Fatalf("%d ci_run_stream rows recorded after the lease was reclaimed, want 0", n)
	}
	if got := len(r.streamEvents(EventKindCheckpointTerminal)); got != 0 || fired != 0 {
		t.Fatalf("terminal events = %d, OnTerminal calls = %d, want 0 and 0", got, fired)
	}
	if n := r.count(r.ciDB, `SELECT COUNT(*) FROM ci_stream_attempt WHERE state = ?`, attemptTombstoned); n != 1 {
		t.Fatalf("tombstoned attempts = %d, want the fenced attempt tombstoned", n)
	}
}

func TestLeaseReclaimedWhileDownIsDroppedByResume(t *testing.T) {
	r := newRig(t)
	r.commit(map[string]string{"docs/a.md": "a"})
	r.exec.fn = func(SubJob) (SubJobResult, error) { return SubJobResult{}, errors.New("daemon went down") }
	if _, err := r.checkpoint(); err == nil {
		t.Fatal("the interrupted checkpoint must report the unfinished sub-jobs")
	}
	ran := r.exec.count()
	r.reclaimLease()
	var fired int
	r.d.OnTerminal(func(CIResultEvent) { fired++ })
	rep, err := r.d.Resume(context.Background())
	if err != nil || rep.Dropped != 2 || rep.Redispatched != 0 {
		t.Fatalf("Resume = %+v, %v; want both rows dropped, none re-dispatched", rep, err)
	}
	if r.exec.count() != ran || fired != 0 || len(r.streamEvents(EventKindCheckpointTerminal)) != 0 {
		t.Fatalf("sub-jobs %d->%d, OnTerminal %d, terminal events %d; a reclaimed lease must run and publish nothing",
			ran, r.exec.count(), fired, len(r.streamEvents(EventKindCheckpointTerminal)))
	}
	if open, err := r.d.unconfirmedKeys(context.Background()); err != nil || len(open) != 0 {
		t.Fatalf("unconfirmed rows = %d (%v), want 0", len(open), err)
	}
}
