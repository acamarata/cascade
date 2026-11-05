package repo

// Purpose: the debounced watcher: coalescing, the closed gap list,
//   Quiesce/Resume, directory-only watches, the watch cap and descriptor
//   release. Debounce runs on an injected clock and injected ticks.
// SPORT: repo/change-feed/ADD test coverage (P1-GEN-14).

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/fsnotify/fsnotify"
)

// chanSource is an in-memory event source the test drives.
type chanSource struct {
	ev   chan fsnotify.Event
	errs chan error
}

func newChanSource() *chanSource {
	return &chanSource{ev: make(chan fsnotify.Event), errs: make(chan error)}
}
func (s *chanSource) Add(string) error              { return nil }
func (s *chanSource) Close() error                  { return nil }
func (s *chanSource) Events() <-chan fsnotify.Event { return s.ev }
func (s *chanSource) Errors() <-chan error          { return s.errs }

// counter counts calls and signals each one.
type counter struct {
	mu      sync.Mutex
	n       int
	batches []Batch
	sig     chan struct{}
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func waitSweeps(t *testing.T, c *counter, n int) {
	t.Helper()
	for c.count() < n {
		select {
		case <-c.sig:
		case <-time.After(30 * time.Second):
			t.Fatalf("waited for %d sweeps, saw %d", n, c.count())
		}
	}
}

// testWatcher wires a Watcher over e with counting sweep and apply seams,
// injected ticks and an in-memory source.
type testWatcher struct {
	*Watcher
	src   *chanSource
	tickC chan time.Time
	clk   *runtime.FixedClock
}

func newTestWatcher(t *testing.T, e *menv) (*testWatcher, *counter, *counter) {
	t.Helper()
	clk := runtime.NewFixedClock(time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC))
	w, err := NewWatcher(e.root, e.mt, clk)
	if err != nil {
		t.Fatal(err)
	}
	sweeps, applies := &counter{sig: make(chan struct{}, 64)}, &counter{sig: make(chan struct{}, 64)}
	w.sweep = func(context.Context) (int, error) {
		sweeps.mu.Lock()
		sweeps.n++
		sweeps.mu.Unlock()
		sweeps.sig <- struct{}{}
		return 0, nil
	}
	w.apply = func(_ context.Context, b Batch) error {
		applies.mu.Lock()
		applies.n++
		applies.batches = append(applies.batches, b)
		applies.mu.Unlock()
		return nil
	}
	tw := &testWatcher{Watcher: w, src: newChanSource(), tickC: make(chan time.Time), clk: clk}
	w.open = func() (eventSource, error) { return tw.src, nil }
	w.tick, w.sweepTick = tw.tickC, make(chan time.Time)
	return tw, sweeps, applies
}

// runWatcher starts Run; the returned stop cancels it and checks its result.
func runWatcher(t *testing.T, w *testWatcher) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}
}

// settle sends a tick and then an ignored .git event: that send completes
// only after the tick was fully handled, and handling it reads no clock.
func (w *testWatcher) settle() {
	w.tickC <- time.Time{}
	w.sync()
}

func (w *testWatcher) sync() {
	w.src.ev <- fsnotify.Event{Name: filepath.Join(w.root, ".git", "sync"), Op: fsnotify.Write}
}

// touch delivers one Write event, then an ignored .git event: the second
// send completes only after the first was handled, so the test may advance
// the clock without racing the watcher's read of it.
func (w *testWatcher) touch(rel string) {
	w.src.ev <- fsnotify.Event{Name: filepath.Join(w.root, filepath.FromSlash(rel)), Op: fsnotify.Write}
	w.sync()
}

func TestWatchDebounceCoalesces(t *testing.T) {
	e := newWatchEnv(t)
	w, sweeps, applies := newTestWatcher(t, e)
	stop := runWatcher(t, w)
	waitSweeps(t, sweeps, 1)
	var want []string
	for i := 0; i < 10; i++ {
		rel := "pkg/f" + string(rune('0'+i)) + ".go"
		w.touch(rel)
		w.touch(rel)
		want = append(want, rel)
		w.clk.Advance(DebounceWindow / 20)
	}
	w.touch(".git/index")
	w.touch("node_modules/x.js")
	w.settle()
	if applies.count() != 0 {
		t.Fatal("a batch was delivered inside the debounce window")
	}
	w.clk.Advance(DebounceWindow)
	w.settle()
	stop()
	if applies.count() != 1 || !reflect.DeepEqual(applies.batches[0].Changed, want) {
		t.Fatalf("batches = %+v, want one with the union %v", applies.batches, want)
	}
	if b := applies.batches[0]; b.FSSeq != 1 || sweeps.count() != 1 {
		t.Fatalf("batch seq %d sweeps %d, want seq 1 after the one start sweep", b.FSSeq, sweeps.count())
	}
}

func TestWatchCursorGapDetected(t *testing.T) {
	gapAfter := func(t *testing.T, e *menv, trigger func(w *testWatcher)) {
		t.Helper()
		w, sweeps, applies := newTestWatcher(t, e)
		stop := runWatcher(t, w)
		waitSweeps(t, sweeps, 1)
		trigger(w)
		w.settle()
		w.settle()
		stop()
		if sweeps.count() != 2 || applies.count() != 0 {
			t.Fatalf("sweeps %d applies %d, want exactly one gap sweep and no batch", sweeps.count(), applies.count())
		}
	}
	pending := func(w *testWatcher) {
		w.touch("a/a.go")
		w.clk.Advance(DebounceWindow)
	}
	t.Run("overflow", func(t *testing.T) {
		gapAfter(t, newWatchEnv(t), func(w *testWatcher) {
			pending(w)
			w.src.errs <- fsnotify.ErrEventOverflow
		})
	})
	t.Run("restart", testRestartSweeps)
	t.Run("unreadable journal", func(t *testing.T) {
		e := newWatchEnv(t)
		gapAfter(t, e, func(w *testWatcher) {
			pending(w)
			if _, err := e.db.Exec(`DROP TABLE repo_watch_cursor`); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("non-descendant commit", func(t *testing.T) {
		e := newWatchEnv(t)
		commitAll(t, e.root, "fixture")
		cur := Cursor{RepositoryID: testRepoID, Commit: "0123456789abcdef0123456789abcdef01234567", Generation: 1}
		if err := e.mt.d.Journal.AdvanceCursor(context.Background(), cur); err != nil {
			t.Fatal(err)
		}
		gapAfter(t, e, pending)
	})
}

// testRestartSweeps: every Run start is a gap swept exactly once.
func testRestartSweeps(t *testing.T) {
	w, sweeps, applies := newTestWatcher(t, newWatchEnv(t))
	runWatcher(t, w)()
	stop := runWatcher(t, w)
	waitSweeps(t, sweeps, 2)
	w.settle()
	stop()
	if sweeps.count() != 2 || applies.count() != 0 {
		t.Fatalf("sweeps %d applies %d, want one sweep per start", sweeps.count(), applies.count())
	}
}

func TestWatcherQuiesceResume(t *testing.T) {
	e := newMaintEnv(t)
	w, sweeps, applies := newTestWatcher(t, e)
	started, release := make(chan struct{}, 4), make(chan struct{})
	counted := w.apply
	w.apply = func(ctx context.Context, b Batch) error {
		started <- struct{}{}
		<-release
		if _, err := e.mt.Apply(ctx, b); err != nil {
			return err
		}
		return counted(ctx, b)
	}
	stop := runWatcher(t, w)
	waitSweeps(t, sweeps, 1)
	e.write(t, "b/b.go", betaChanged)
	w.touch("b/b.go")
	w.clk.Advance(DebounceWindow)
	w.tickC <- time.Time{}
	<-started
	res := make(chan Cursor, 1)
	go func() {
		cur, err := w.Quiesce(context.Background())
		if err != nil {
			t.Error(err)
		}
		res <- cur
	}()
	select {
	case <-res:
		t.Fatal("Quiesce returned while a batch was in flight")
	default:
	}
	close(release)
	if cur := <-res; cur.FSSeq != 1 || cur.Generation != 1 {
		t.Fatalf("quiesced cursor = %+v, want the in-flight batch's commit", cur)
	}
	w.touch("a/a.go")
	w.clk.Advance(DebounceWindow)
	w.settle()
	if applies.count() != 1 {
		t.Fatalf("a batch was delivered while quiesced (%d)", applies.count())
	}
	w.Resume()
	w.settle()
	stop()
	if applies.count() != 2 || !reflect.DeepEqual(applies.batches[1].Changed, []string{"a/a.go"}) {
		t.Fatalf("after Resume batches = %+v, want a/a.go delivered", applies.batches)
	}
	quiesceCanceled(t, w.Watcher)
}

// quiesceCanceled: a canceled Quiesce fails while a batch is in flight.
func quiesceCanceled(t *testing.T, w *Watcher) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.mu.Lock()
	w.inflight = make(chan struct{})
	w.mu.Unlock()
	if _, err := w.Quiesce(ctx); err == nil {
		t.Fatal("Quiesce with a canceled context must fail while a batch is in flight")
	}
}

// newWatchEnv is an unseeded maintenance env: the watcher tests that stub
// the sweep need only the journal, so they skip the inventory and graph seed.
func newWatchEnv(t *testing.T) *menv {
	t.Helper()
	return openEnv(t, newMaintRepo(t), filepath.Join(t.TempDir(), "watch.db"), "", false)
}

// mkdirs creates each slash path under root.
func mkdirs(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(r)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
