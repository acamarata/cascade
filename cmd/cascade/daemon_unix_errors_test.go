//go:build !windows

// Purpose: platformDaemonStop/Status/Start/Restart error-branch and
//
//	stopOptions unit tests — split out of daemon_unix_cmd_test.go under
//	R-14.117/R-14.133 (Art.10.3's 300-line file cap; that file grew past
//	it once this ticket closed cmd/cascade's remaining CLI-tier coverage
//	gaps). Same package, no behaviour change.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates; R-14.117 sibling
//
//	split of daemon_unix_cmd_test.go).
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestPlatformDaemonStop_ConfigError covers platformDaemonStop's
// loadDaemonConfig failure branch — the counterpart to
// TestDaemonRunCmd_ConfigError/TestDaemonStartCmd_ConfigError/
// TestDaemonRestartCmd_ConfigError above, which do not exercise stop or
// status.
func TestPlatformDaemonStop_ConfigError(t *testing.T) {
	deps := newTestDaemonDeps(t, "this is not [ valid toml")
	_, err := platformDaemonStop(context.Background(), deps)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestPlatformDaemonStatus_ConfigError is TestPlatformDaemonStop_ConfigError's
// counterpart for platformDaemonStatus.
func TestPlatformDaemonStatus_ConfigError(t *testing.T) {
	deps := newTestDaemonDeps(t, "this is not [ valid toml")
	_, err := platformDaemonStatus(context.Background(), deps)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestPlatformDaemonStart_ExecutableError covers platformDaemonStart's
// deps.Executable() failure branch: a valid config.toml but an
// Executable func that errors must surface that error before ever
// attempting ensureSpawnDirs or a real spawn.
func TestPlatformDaemonStart_ExecutableError(t *testing.T) {
	deps := newTestDaemonDeps(t, "")
	boom := errors.New("cannot resolve executable path")
	deps.Executable = func() (string, error) { return "", boom }
	_, err := platformDaemonStart(context.Background(), deps)
	if err == nil || !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// TestPlatformDaemonRestart_ExecutableError is
// TestPlatformDaemonStart_ExecutableError's counterpart for
// platformDaemonRestart.
func TestPlatformDaemonRestart_ExecutableError(t *testing.T) {
	deps := newTestDaemonDeps(t, "")
	boom := errors.New("cannot resolve executable path")
	deps.Executable = func() (string, error) { return "", boom }
	_, err := platformDaemonRestart(context.Background(), deps)
	if err == nil || !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// TestPlatformDaemonStart_EnsureSpawnDirsError covers platformDaemonStart's
// ensureSpawnDirs failure branch: deps.Executable succeeds, but the
// LogDir a real spawn's log file would live under collides with an
// existing regular file, so MkdirAll fails before any process is spawned.
func TestPlatformDaemonStart_EnsureSpawnDirsError(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if err := os.WriteFile(filepath.Join(root, "logs"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed colliding file: %v", err)
	}
	deps := daemonDeps{
		Paths:      paths,
		Getenv:     func(string) string { return "" },
		Environ:    func() []string { return nil },
		Executable: func() (string, error) { return "/bin/true", nil },
	}
	_, err := platformDaemonStart(context.Background(), deps)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestPlatformDaemonRestart_EnsureSpawnDirsError is
// TestPlatformDaemonStart_EnsureSpawnDirsError's counterpart for
// platformDaemonRestart — the Stop half must complete (nothing is running,
// so it is idempotent) before Restart ever reaches its own
// ensureSpawnDirs call.
func TestPlatformDaemonRestart_EnsureSpawnDirsError(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if err := os.WriteFile(filepath.Join(root, "logs"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed colliding file: %v", err)
	}
	deps := daemonDeps{
		Paths:      paths,
		Getenv:     func(string) string { return "" },
		Environ:    func() []string { return nil },
		Executable: func() (string, error) { return "/bin/true", nil },
	}
	_, err := platformDaemonRestart(context.Background(), deps)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}

// TestStopOptions_BuildsAWorkingSocketGoneClosure covers stopOptions'
// SocketGone closure body directly — every other test drives Stop/Restart
// through daemon.Stop itself, which only calls SocketGone() once nothing
// is already running has been ruled out (Art.7.1-friendly: no real PID or
// signal involved, just the filesystem predicate).
func TestStopOptions_BuildsAWorkingSocketGoneClosure(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	settings := daemon.Settings{SocketPath: filepath.Join(root, "daemon.sock")}
	opts := stopOptions(paths, settings)

	if !opts.SocketGone() {
		t.Error("SocketGone() = false for a socket path that was never created")
	}
	if err := os.WriteFile(settings.SocketPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if opts.SocketGone() {
		t.Error("SocketGone() = true for a socket path that exists")
	}
}
