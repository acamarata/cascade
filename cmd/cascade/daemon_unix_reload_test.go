//go:build !windows

// Purpose: end-to-end proof of wireHotReload — the actual production
//
//	composition-root function daemon_unix.go's platformDaemonRun calls —
//	against a real openRuntimeStore-opened cascade.db. Two things
//	R-14.175 named as built, tested, and never wired: the StoreAuditRecorder
//	never attached to the daemon's store, and the fsnotify watcher never
//	started. This file proves both through wireHotReload itself, not
//	through internal/runtime's own component tests.
//
// SPORT: cmd/cascade/daemon (CHANGED — R-14.175 hot-reload wiring
//
//	verification).
package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

const testReloadConfigBody = "[conductor]\nexternal_routing_enabled = true\n"

// TestWireHotReload_AuditRecordPersistedAfterReload drives wireHotReload
// against a real store, forces one real Reload (config.toml rewritten
// on disk with a change), and asserts a "reload_accepted" audit record
// lands in the SAME store openRuntimeStore opened — proving
// runtime.NewStoreAuditRecorder is genuinely attached, not merely
// constructible.
func TestWireHotReload_AuditRecordPersistedAfterReload(t *testing.T) {
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
	t.Cleanup(watcher.Stop)

	// Rewrite config.toml with a different (still valid, still
	// non-cold-key) value so Reload has something new to accept, and
	// call Reload directly — this test's own outcome check, not the
	// fsnotify path (that is TestWireHotReload_WatcherObservesFileEdit's
	// job, separately, integration-tagged).
	newBody := "[conductor]\nexternal_routing_enabled = false\n"
	if err := os.WriteFile(paths.ConfigPath(), []byte(newBody), 0o600); err != nil {
		t.Fatalf("rewrite config.toml: %v", err)
	}
	outcome := hr.Reload(ctx)
	if !outcome.Accepted {
		t.Fatalf("Reload: want Accepted, got %+v", outcome)
	}

	assertAuditRecordExists(ctx, t, store, "reload_accepted/")
}

// newReloadTestStore creates a fresh temp CASCADE_HOME, seeds an initial
// config.toml, and opens the real production store over it — the common
// setup both the direct-Reload test above and the fsnotify integration
// test share.
func newReloadTestStore(t *testing.T) (fakeDaemonPaths, *testkit.FrozenClock, provider.Store) {
	t.Helper()
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	ctx := context.Background()

	if err := os.MkdirAll(paths.Root(), 0o700); err != nil {
		t.Fatalf("MkdirAll root: %v", err)
	}
	if err := os.WriteFile(paths.ConfigPath(), []byte(testReloadConfigBody), 0o600); err != nil {
		t.Fatalf("write initial config.toml: %v", err)
	}

	store, _, closeStore, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		t.Fatalf("openRuntimeStore: %v", err)
	}
	t.Cleanup(closeStore)
	return paths, clock, store
}

// assertAuditRecordExists fails the test if store's "audit" namespace has
// no record under prefix.
func assertAuditRecordExists(ctx context.Context, t *testing.T, store provider.Store, prefix string) {
	t.Helper()
	it, err := store.Scan(ctx, "audit", prefix)
	if err != nil {
		t.Fatalf("Scan audit namespace: %v", err)
	}
	defer func() { _ = it.Close() }()

	found := false
	for it.Next(ctx) {
		found = true
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterate audit records: %v", err)
	}
	if !found {
		t.Errorf("no %q audit record found in the real store — StoreAuditRecorder is not actually attached", prefix)
	}
}

// TestWireHotReload_WatcherObservesFileEdit is the integration-tagged
// (real fsnotify, real files) proof that editing config.toml on disk
// while the watcher wireHotReload started is running is actually
// observed — the fourth R-14.175 gap this ticket closes, closed as
// wireHotReload wires it (a short, injected, non-default debounce, not
// the production 500ms, so this test does not need to wait that long;
// the wait for the resulting event uses a short poll ticker rather than
// one blocking sleep, matching internal/runtime/hotreload_watch_test.go's
// own established pattern for this exact watcher). See
// daemon_unix_reload_integration_test.go.
