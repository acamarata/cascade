package repo

// Purpose: the debounced fsnotify change source feeding Maintenance.Apply,
//   with gap detection, a periodic bounded sweep and Quiesce/Resume
//   (contract:repo-change-feed).
// Inputs: a repository root, its Maintenance pipeline and an injected Clock.
// Outputs: Batches applied through the pipeline; sweeps on every gap.
// Constraints: directories only are watched (fsnotify is not recursive),
//   skipping .git and every ExcludedPath segment, at most MaxWatches; the
//   closed gap list (fsnotify overflow or error, watcher start, unreadable
//   journal, ErrGap) each schedule exactly one Sweep before the next batch;
//   the debounce reads the injected clock, never the wall clock.
// SPORT: repo/change-feed/ADD (contract:repo-change-feed, P1-GEN-14).

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/fsnotify/fsnotify"
)

// DebounceWindow is the quiet period that closes a batch of events.
const DebounceWindow = 500 * time.Millisecond

// MaxWatches caps the watched directories per repository; past it the
// repository runs sweep-only with one watch-capped attention item.
const MaxWatches = 4096

// eventSource is the fsnotify surface the watcher reads.
type eventSource interface {
	Add(dir string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

// fsnSource adapts *fsnotify.Watcher to eventSource.
type fsnSource struct{ w *fsnotify.Watcher }

func (s fsnSource) Add(dir string) error          { return s.w.Add(dir) }
func (s fsnSource) Close() error                  { return s.w.Close() }
func (s fsnSource) Events() <-chan fsnotify.Event { return s.w.Events }
func (s fsnSource) Errors() <-chan error          { return s.w.Errors }

func openFSNotify() (eventSource, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "repo: fsnotify watcher failed to start")
	}
	return fsnSource{w}, nil
}

// Watcher feeds one repository's Maintenance pipeline from fsnotify events.
type Watcher struct {
	root  string
	m     *Maintenance
	clock runtime.Clock
	rec   *Reconciler

	// seams: the event source, the batch and sweep actions and the tickers.
	open       func() (eventSource, error)
	apply      func(ctx context.Context, b Batch) error
	sweep      func(ctx context.Context) (int, error)
	tick       <-chan time.Time
	sweepTick  <-chan time.Time
	maxWatches int
	onWatch    func(dir string)

	// loop state, owned by Run's goroutine.
	src     eventSource
	watched map[string]bool
	capped  bool
	pending map[string]bool
	last    time.Time
	gap     bool

	mu       sync.Mutex
	quiesced bool
	inflight chan struct{}
}

// NewWatcher builds a Watcher over m for root; the debounce uses clock.
func NewWatcher(root string, m *Maintenance, clock runtime.Clock) (*Watcher, error) {
	if root == "" || m == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "repo: watcher requires a root, a maintenance pipeline and a clock")
	}
	rec, err := NewReconciler(m, m.d.Journal, m.d.Attention)
	if err != nil {
		return nil, err
	}
	w := &Watcher{root: root, m: m, clock: clock, rec: rec, open: openFSNotify, maxWatches: MaxWatches, onWatch: func(string) {}}
	w.apply = func(ctx context.Context, b Batch) error { _, err := m.Apply(ctx, b); return err }
	w.sweep = rec.Sweep
	return w, nil
}

// Run watches until ctx is done and closes the fsnotify watcher on return.
// Start is a gap: the first action is a Sweep (which replays). Events
// coalesce for DebounceWindow on the injected clock; an fsnotify overflow or
// error is a gap swept before the next batch.
func (w *Watcher) Run(ctx context.Context) error {
	src, err := w.open()
	if err != nil {
		return err
	}
	w.src, w.watched, w.capped, w.pending = src, map[string]bool{}, false, map[string]bool{}
	defer func() { _ = src.Close() }()
	tick, sweepTick, stop := w.tickers()
	defer stop()
	w.addTree(w.root)
	if w.capped {
		if err := w.capAttention(ctx); err != nil {
			return err
		}
	}
	w.gap = true
	if err := w.flush(ctx); err != nil {
		return err
	}
	for {
		if err := w.step(ctx, tick, sweepTick); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// step handles one loop event; an error ends Run.
func (w *Watcher) step(ctx context.Context, tick, sweepTick <-chan time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ev, ok := <-w.src.Events():
		if !ok {
			return cascade.New(cascade.KindUnavailable, "repo: fsnotify event stream closed")
		}
		w.event(ev)
	case _, ok := <-w.src.Errors():
		if !ok {
			return cascade.New(cascade.KindUnavailable, "repo: fsnotify error stream closed")
		}
		w.gap = true // overflow or any watcher error: events were lost
	case <-tick:
		return w.flush(ctx)
	case <-sweepTick:
		w.gap = true
		return w.flush(ctx)
	}
	return nil
}

// tickers returns the injected tickers, or real ones with their stop.
func (w *Watcher) tickers() (tick, sweepTick <-chan time.Time, stop func()) {
	tick, sweepTick = w.tick, w.sweepTick
	var stops []func()
	if tick == nil {
		t := time.NewTicker(DebounceWindow / 5)
		tick, stops = t.C, append(stops, t.Stop)
	}
	if sweepTick == nil {
		t := time.NewTicker(ReconcileInterval)
		sweepTick, stops = t.C, append(stops, t.Stop)
	}
	return tick, sweepTick, func() {
		for _, s := range stops {
			s()
		}
	}
}

// event records one fsnotify event; a created directory is watched too.
func (w *Watcher) event(ev fsnotify.Event) {
	rel, ok := w.rel(ev.Name)
	if !ok {
		return
	}
	if ev.Has(fsnotify.Create) && w.addTree(ev.Name) {
		return
	}
	w.pending[rel] = true
	w.last = w.clock.Now()
}

// rel maps an absolute event path to an ingestable slash repo path.
func (w *Watcher) rel(name string) (string, bool) {
	r, err := filepath.Rel(w.root, name)
	if err != nil || ExcludedPath(r) {
		return "", false
	}
	return filepath.ToSlash(r), true
}

// addTree watches dir and every non-excluded directory below it, marking
// files found under a newly created directory pending. It reports whether
// dir is a directory. Past maxWatches it marks the watcher capped.
func (w *Watcher) addTree(dir string) bool {
	isDir := false
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a vanished entry is picked up by the next event or sweep
		}
		rel, ok := w.rel(p)
		if p != w.root && !ok {
			return skipIfDir(d)
		}
		if p == dir {
			isDir = d.IsDir()
		}
		if !d.IsDir() {
			if p != dir && dir != w.root {
				w.pending[rel], w.last = true, w.clock.Now()
			}
			return nil
		}
		return w.watchDir(p)
	})
	return isDir
}

// watchDir adds one directory watch, or stops the walk at the cap.
func (w *Watcher) watchDir(p string) error {
	if w.capped || w.watched[p] {
		return nil
	}
	if len(w.watched) >= w.maxWatches {
		w.capped = true
		return filepath.SkipAll
	}
	if err := w.src.Add(p); err != nil {
		w.gap = true // an unwatched directory loses events
		return filepath.SkipDir
	}
	w.watched[p] = true
	w.onWatch(p)
	return nil
}

func skipIfDir(d fs.DirEntry) error {
	if d.IsDir() {
		return filepath.SkipDir
	}
	return nil
}

// capAttention raises the one watch-capped item and releases every watch:
// the repository runs sweep-only from here.
func (w *Watcher) capAttention(ctx context.Context) error {
	_ = w.src.Close()
	w.src = closedSource{}
	w.watched = map[string]bool{}
	return w.m.d.Attention(ctx, "repo.watch-capped:"+w.m.d.RepositoryID,
		"more than the watch limit of directories; the repository runs sweep-only")
}

// closedSource is the event source of a sweep-only watcher: it never fires.
type closedSource struct{}

func (closedSource) Add(string) error              { return nil }
func (closedSource) Close() error                  { return nil }
func (closedSource) Events() <-chan fsnotify.Event { return nil }
func (closedSource) Errors() <-chan error          { return nil }

// pendingRels returns the pending paths, sorted.
func (w *Watcher) pendingRels() []string {
	out := make([]string, 0, len(w.pending))
	for r := range w.pending {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}
