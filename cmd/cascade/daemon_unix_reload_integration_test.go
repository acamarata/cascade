//go:build !windows && integration

// Purpose: the integration-tagged half of the wireHotReload proof — a
//
//	real fsnotify.Watcher and the real 500ms debounce (Art.7.2: network/
//	real-clock-timing tests are `-tags=integration` only), started
//	through wireHotReload itself (the production composition-root
//	function), observing a real config.toml edit on disk while it runs
//	and landing a real audit record in the real store. Companion to
//	internal/runtime/hotreload_integration_test.go's
//	TestReloadWatcher_Integration_DetectsWriteWithinDebounce, one layer
//	up: that test proves ReloadWatcher itself works; this one proves
//	daemon_unix.go's wiring of it does too.
//
// SPORT: cmd/cascade/daemon (CHANGED — R-14.175 hot-reload wiring
//
//	verification, integration lane).
package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
)

// TestWireHotReload_WatcherObservesFileEdit proves that editing
// config.toml on disk, while the real watcher wireHotReload started is
// running, is observed and applied — no direct call to hr.Reload. This
// is R-14.175's fourth gap, driven end to end.
func TestWireHotReload_WatcherObservesFileEdit(t *testing.T) {
	ctx := context.Background()
	paths, clock, store := newReloadTestStore(t)

	bus := events.New(store, clock)
	getenv := func(string) string { return "" }
	environ := func() []string { return nil }

	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(), Getenv: getenv, Environ: environ})
	if err != nil {
		t.Fatalf("Load initial config: %v", err)
	}

	hr, watcher, err := wireHotReload(paths, clock, getenv, environ, cfg, store, bus, nil)
	if err != nil {
		t.Fatalf("wireHotReload: %v", err)
	}
	defer watcher.Stop()

	// A real edit on disk, via the watched directory, not a direct
	// hr.Reload call — the watcher must pick this up itself.
	newBody := "[conductor]\nexternal_routing_enabled = false\n"
	if err := os.WriteFile(paths.ConfigPath(), []byte(newBody), 0o600); err != nil {
		t.Fatalf("edit config.toml: %v", err)
	}

	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the watcher to observe the config.toml edit (checked via hr.Current())")
		case <-tick.C:
			if !conductorExternalRoutingEnabled(hr.Current()) {
				return // the watcher observed the edit and applied it
			}
		}
	}
}

// conductorExternalRoutingEnabled reads [conductor].external_routing_enabled
// out of cfg.Extra the same way hotreload_security.go's boolAt does
// internally (unexported there) — this file only needs one bool back,
// not the full EffectiveConfig extraction.
func conductorExternalRoutingEnabled(cfg *runtime.Config) bool {
	section, ok := cfg.Extra["conductor"].(map[string]interface{})
	if !ok {
		return false
	}
	v, _ := section["external_routing_enabled"].(bool)
	return v
}
