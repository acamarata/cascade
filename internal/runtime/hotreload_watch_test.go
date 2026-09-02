package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Purpose: fast, non-integration-tagged coverage of ReloadWatcher's
//   lifecycle (Start/Stop/loop/resetTimer) using a real fsnotify.Watcher
//   but a short debounce, so it runs in the default (non "integration"
//   tagged) test lane the ticket's coverage-floor check uses. The
//   contract's tagged hotreload_integration_test.go additionally proves
//   the real 08 §3 500ms debounce end to end; this file exists only so
//   the watcher's own code is exercised without waiting 500ms+ per run.

func TestReloadWatcher_StartStopLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = false\n")

	opts := LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)}
	initial, err := Load(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	events := &fakeEventPublisher{}
	hr := NewHotReloader(path, opts, initial, NewFixedClock(time.Unix(0, 0)), events, nil, nil)

	w := NewReloadWatcher(hr, path, 5*time.Millisecond)
	if err := w.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = false\n")

	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
loop:
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for a reload event; got %v", events.names())
		case <-tick.C:
			for _, n := range events.names() {
				if n == eventReloadAccepted {
					break loop
				}
			}
		}
	}

	w.Stop()
	w.Stop() // idempotent: a second Stop must not panic or block
}

func TestReloadWatcher_ZeroDebounceUsesDefault(t *testing.T) {
	hr, path, _, _, _ := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")
	w := NewReloadWatcher(hr, path, 0)
	if w.debounce != defaultDebounce {
		t.Fatalf("expected default debounce, got %v", w.debounce)
	}
}

func TestReloadWatcher_StopBeforeStartIsNoop(t *testing.T) {
	hr, path, _, _, _ := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")
	w := NewReloadWatcher(hr, path, time.Millisecond)
	w.Stop() // must not panic when never started
}

func TestReloadWatcher_StartOnMissingDirFails(t *testing.T) {
	hr, path, _, _, _ := newTestHotReloader(t, "[conductor]\nexternal_routing_enabled = false\n")
	w := NewReloadWatcher(hr, filepath.Join(path, "..", "does-not-exist", "config.toml"), time.Millisecond)
	if err := w.Start(); err == nil {
		w.Stop()
		t.Fatal("expected Start to fail for a nonexistent directory")
	}
}
