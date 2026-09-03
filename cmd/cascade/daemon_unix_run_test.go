//go:build !windows

// Purpose: coverage for daemon_unix.go's platformDaemonRun happy path and
//
//	platformDaemonStart/Restart's daemon.Start()-failure branch —
//	daemon_unix_errors_test.go already covers the deps.Executable() and
//	ensureSpawnDirs() error branches; this file closes the two gaps that
//	are left: the loadDaemonConfig SUCCESS path all the way through
//	daemon.Run, and the spawn-attempt failure daemon.Start/Restart itself
//	can return. Uses injected fakes exactly like daemon_unix_cmd_test.go's
//	newTestDaemonDeps rather than the real-binary spawn daemon_test.go's
//	end-to-end test uses (that test's separate process is invisible to
//	`go tool cover` — see daemon_unix_test.go's own doc comment for why
//	these direct-call tests exist at all).
//
// Constraints: no test spawns a real daemon process or binds a real socket
//
//	outside t.TempDir() — the "spawn error" tests point deps.Executable at
//	a path that does not exist, so exec fails before any process is
//	created; the "run" test cancels its context before platformDaemonRun's
//	daemon.Run call so the real (but t.TempDir()-rooted) socket it briefly
//	binds is torn down immediately.
//
// SPORT: cmd/cascade/daemon (coverage-only addition, no sport_updates —
//
//	this file adds no new production surface).
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newRunTestDeps builds a daemonDeps rooted at a fresh short temp dir (not
// t.TempDir(), whose path embeds this test's own long name — the daemon.
// Run call this file's happy-path test drives binds a REAL unix socket
// there, and that path must fit in sockaddr_un.sun_path, ~104 bytes on
// Darwin; same fix as daemon_test.go's shortHomeDir), with a real
// runtime.Clock (platformDaemonRun/Start/Restart all thread it into
// internal/runtime and internal/daemon calls that need a working clock,
// unlike loadDaemonConfig alone) and the given Executable func.
func newRunTestDeps(t *testing.T, executable func() (string, error)) daemonDeps {
	t.Helper()
	dir, err := os.MkdirTemp("", "cascd")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return daemonDeps{
		Paths:      fakeDaemonPaths{root: dir},
		Getenv:     func(string) string { return "" },
		Environ:    func() []string { return nil },
		Clock:      runtime.SystemClock{},
		Executable: executable,
	}
}

// nonexistentExecutable always resolves to a path nothing occupies, so
// daemon.DefaultSpawn's exec.Command(...).Start() fails immediately (ENOENT)
// without ever creating a process (spawn fails before any socket bind is
// attempted, so this path need not satisfy sockaddr_un's length limit).
func nonexistentExecutable(t *testing.T) func() (string, error) {
	t.Helper()
	dir := t.TempDir()
	return func() (string, error) { return filepath.Join(dir, "no-such-binary"), nil }
}

// TestPlatformDaemonRun_ReturnsOnContextCancel proves the loadDaemonConfig
// SUCCESS path through NewLogProvider and the daemon.Run call itself: a
// bounded timeout (rather than a pre-canceled context, which
// runtime.Load's own ctx.Err() check would reject before ever reaching
// daemon.Run) makes Run's internal select fire on ctx.Done() once the
// deadline passes, so this returns nil without a real termination signal.
func TestPlatformDaemonRun_ReturnsOnContextCancel(t *testing.T) {
	deps := newRunTestDeps(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := platformDaemonRun(ctx, deps); err != nil {
		t.Fatalf("platformDaemonRun with a bounded-timeout context: %v", err)
	}
}

// TestPlatformDaemonStart_SpawnError covers the daemon.Start() error
// branch: everything up to the spawn attempt succeeds, but the resolved
// executable path does not exist, so DefaultSpawn's exec.Command(...).
// Start() fails before any process or socket is created.
func TestPlatformDaemonStart_SpawnError(t *testing.T) {
	root := t.TempDir()
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	deps.Paths = fakeDaemonPaths{root: root}
	_, err := platformDaemonStart(context.Background(), deps)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable from the failed spawn", err)
	}
}

// TestPlatformDaemonRestart_SpawnError covers the daemon.Restart() error
// branch reached through Start: with nothing running, Restart's internal
// Stop phase is an instant idempotent no-op, so this exercises the same
// spawn-failure path as TestPlatformDaemonStart_SpawnError but through
// Restart's own StartOptions wiring.
func TestPlatformDaemonRestart_SpawnError(t *testing.T) {
	root := t.TempDir()
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	deps.Paths = fakeDaemonPaths{root: root}
	_, err := platformDaemonRestart(context.Background(), deps)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable from the failed spawn", err)
	}
}
