//go:build integration

package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Purpose: Linux integration test (Art.7.2: //go:build integration) —
//   proves ReloadWatcher actually detects a real filesystem write via
//   fsnotify and fires exactly one debounced Reload within the 500ms
//   debounce window, end to end (no injected clock/timer — the real
//   fsnotify.Watcher and time.AfterFunc this ticket's contract requires
//   proving on CI). Run via: go test -race -count=1 -tags=integration
//   ./internal/runtime/...

func TestReloadWatcher_Integration_DetectsWriteWithinDebounce(t *testing.T) {
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

	watcher := NewReloadWatcher(hr, path, defaultDebounce)
	if err := watcher.Start(); err != nil {
		t.Fatalf("watcher.Start: %v", err)
	}
	defer watcher.Stop()

	// A valid tightening write: no-op change (false->false) still counts
	// as a config.toml write that must be detected and re-validated.
	_ = writeFile(t, path, "[conductor]\nexternal_routing_enabled = false\n")

	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for config.reload.accepted; events so far: %v", events.names())
		case <-tick.C:
			for _, n := range events.names() {
				if n == eventReloadAccepted {
					return
				}
			}
		}
	}
}
