package daemon

// Purpose: a teardown registered after Close ran is run at once and the build
// step fails closed, so the jobs DB handle is closed on every path (P1-CI-22).
// The windows-sensitive fact (a leaked handle keeps cascade.db undeletable) is
// asserted on every OS through the close seam and a delete of the file.
// SPORT: internal/daemon (ADD, P1-CI-22).

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestAddOrRunCloserRunsALateCloserAtOnce: before Close a closer is only
// registered; Close runs it once; after Close a closer runs at once, exactly
// once, returns its error and is never registered.
func TestAddOrRunCloserRunsALateCloserAtOnce(t *testing.T) {
	r := &FleetRuntime{}
	var early, late atomic.Int32
	if ran, err := r.addOrRunCloser(func() error { early.Add(1); return nil }); ran || err != nil || early.Load() != 0 {
		t.Fatalf("open runtime: ran=%v err=%v early=%d; want registered and not run", ran, err, early.Load())
	}
	if err := r.Close(); err != nil || early.Load() != 1 {
		t.Fatalf("Close = %v, early closer ran %d times; want once", err, early.Load())
	}
	boom := errors.New("boom")
	ran, err := r.addOrRunCloser(func() error { late.Add(1); return boom })
	if !ran || !errors.Is(err, boom) || late.Load() != 1 {
		t.Fatalf("after Close: ran=%v err=%v late=%d; want run at once with its error", ran, err, late.Load())
	}
	_ = r.Close()
	if early.Load() != 1 || late.Load() != 1 || len(r.closers) != 1 {
		t.Fatalf("second Close: early=%d late=%d closers=%d; want 1, 1, 1", early.Load(), late.Load(), len(r.closers))
	}
}

// TestRegisterOrFailFailsClosed: a closer registered after Close ran fails the
// step with a KindUnavailable error and has run; an open runtime returns nil.
func TestRegisterOrFailFailsClosed(t *testing.T) {
	r := &FleetRuntime{}
	if err := r.registerOrFail(func() error { return nil }); err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	_ = r.Close()
	var ran bool
	err := r.registerOrFail(func() error { ran = true; return nil })
	if k, _ := cascade.KindOf(err); err == nil || k != cascade.KindUnavailable || !errors.Is(err, errClosedDuringBuild) || !ran {
		t.Fatalf("after Close: err=%v ran=%v; want KindUnavailable closed-during-build with the closer run", err, ran)
	}
}

// TestAddOrRunCloserRacingCloseRunsEveryCloserOnce: closers registered while
// Close runs each run exactly once, from Close's snapshot or at once, never
// both and never neither. Run with -race and -count.
func TestAddOrRunCloserRacingCloseRunsEveryCloserOnce(t *testing.T) {
	const n = 64
	r := &FleetRuntime{}
	var runs [n]atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = r.addOrRunCloser(func() error { runs[i].Add(1); return nil })
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); <-start; _ = r.Close() }()
	close(start)
	wg.Wait()
	for i := range runs {
		if got := runs[i].Load(); got != 1 {
			t.Fatalf("closer %d ran %d times; want exactly once", i, got)
		}
	}
}

// TestBuildReservationCloseWinsLeavesNoOpenHandle: Close runs between the jobs
// DB open and its closer registration (the window the sampler body or a daemon
// stop reaches). NewFleetRuntime fails closed, the handle is closed exactly
// once, and cascade.db can be deleted (a leaked handle fails that on windows).
func TestBuildReservationCloseWinsLeavesNoOpenHandle(t *testing.T) {
	var closes atomic.Int32
	var opened *sql.DB
	savedClose, savedHook := closeJobsDB, afterJobsDBOpen
	closeJobsDB = func(db *sql.DB) error { opened = db; closes.Add(1); return db.Close() }
	afterJobsDBOpen = func(r *FleetRuntime) { _ = r.Close() }
	t.Cleanup(func() { closeJobsDB, afterJobsDBOpen = savedClose, savedHook })
	g := newFleetRig(t, "")
	r, err := NewFleetRuntime(g.ctx, g.deps)
	if k, _ := cascade.KindOf(err); err == nil || r != nil || k != cascade.KindUnavailable || !errors.Is(err, errClosedDuringBuild) {
		t.Fatalf("NewFleetRuntime = (runtime %v), %v; want a KindUnavailable closed-during-build failure", r != nil, err)
	}
	if n := closes.Load(); n != 1 {
		t.Fatalf("jobs DB closed %d times, want exactly once", n)
	}
	if opened == nil || opened.Ping() == nil {
		t.Fatal("the jobs DB handle is still usable after the failed build")
	}
	g.cancel()
	g.awaitJoin(t)
	if err := os.Remove(filepath.Join(g.deps.Paths.DataDir(), "cascade.db")); err != nil {
		t.Fatalf("delete cascade.db after the failed build: %v (a handle leaked)", err)
	}
}
