package runtime

// Purpose: the fsnotify-backed watch loop that drives HotReloader.Reload
//
//	automatically on a config.toml write, with a 500ms debounce (08 §3:
//	"daemon fsnotify-watches config.toml (500ms debounce)"). Split out of
//	hotreload.go per R-14.117/Art.10.3.
//
// Inputs: the config.toml path (watched via its parent directory, the
//
//	portable pattern for editors/tools that write via
//	temp-file-then-rename — a direct file watch misses the replacement
//	file's new inode on most editors); a Debounce duration (default
//	500ms, overridable so tests never wait a real 500ms — Art.11: no
//	sleep-based flakiness, this is a configuration knob, not a sleep).
//
// Outputs: none directly — every detected, debounced change is handed to
//
//	HotReloader.Reload, whose own events/audit trail is the observable
//	result.
//
// Constraints: no bare time.Now/time.Since (Art.7.3/forbidigo); the
//
//	debounce timer uses time.AfterFunc, which forbidigo's denied-call set
//	does NOT include (see internal/build/clockgate.go's doc comment,
//	which enumerates exactly what the ban does and does not cover).
//
// SPORT: runtime/hot-reload-engine (ADD, placeholder per T-8 sport_updates).

import (
	"context"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// defaultDebounce is 08 §3's specified fsnotify debounce window.
const defaultDebounce = 500 * time.Millisecond

// ReloadWatcher watches path's parent directory for changes to path's
// base name and calls HotReloader.Reload, debounced.
type ReloadWatcher struct {
	hr       *HotReloader
	path     string
	debounce time.Duration

	mu      sync.Mutex
	fsw     *fsnotify.Watcher
	timer   *time.Timer
	stopped chan struct{}
	done    chan struct{}
}

// NewReloadWatcher builds a ReloadWatcher over hr for hr's config path. A
// zero debounce uses defaultDebounce.
func NewReloadWatcher(hr *HotReloader, path string, debounce time.Duration) *ReloadWatcher {
	if debounce <= 0 {
		debounce = defaultDebounce
	}
	return &ReloadWatcher{hr: hr, path: path, debounce: debounce}
}

// Start begins watching. It returns once the watch is established (or
// fails to establish); the debounced reload loop runs in a background
// goroutine until Stop is called.
func (w *ReloadWatcher) Start() error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dir := filepath.Dir(w.path)
	if err := fsw.Add(dir); err != nil {
		_ = fsw.Close()
		return err
	}

	w.mu.Lock()
	w.fsw = fsw
	w.stopped = make(chan struct{})
	w.done = make(chan struct{})
	w.mu.Unlock()

	go w.loop()
	return nil
}

// Stop closes the underlying fsnotify watcher and waits for the loop
// goroutine to exit.
func (w *ReloadWatcher) Stop() {
	w.mu.Lock()
	if w.stopped == nil {
		w.mu.Unlock()
		return
	}
	select {
	case <-w.stopped:
	default:
		close(w.stopped)
	}
	fsw := w.fsw
	done := w.done
	w.mu.Unlock()

	if fsw != nil {
		_ = fsw.Close()
	}
	if done != nil {
		<-done
	}
}

// loop is the watcher goroutine: it filters fsnotify events to ones that
// touch w.path's base name, and (re)starts a debounce timer on each
// relevant event, firing exactly one Reload per quiet period.
func (w *ReloadWatcher) loop() {
	defer close(w.done)
	base := filepath.Base(w.path)
	fireCh := make(chan struct{}, 1)

	for {
		select {
		case <-w.stopped:
			w.stopTimer()
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if filepath.Base(ev.Name) != base {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			w.resetTimer(fireCh)
		case <-fireCh:
			w.hr.Reload(context.Background())
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
		}
	}
}

// resetTimer (re)arms the debounce timer so a burst of events (a
// temp-file-then-rename write shows up as multiple fsnotify events)
// collapses into a single Reload after w.debounce of quiet.
func (w *ReloadWatcher) resetTimer(fireCh chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, func() {
		select {
		case fireCh <- struct{}{}:
		default:
		}
	})
}

func (w *ReloadWatcher) stopTimer() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
}
