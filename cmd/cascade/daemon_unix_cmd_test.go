//go:build !windows

// Purpose: unit tests for daemon.go's loadDaemonConfig and the `daemon run`/
//
//	`start`/`restart` cobra RunE closures' error path — the branches
//	TestDaemonCmd_* in daemon_test.go never reaches because every case
//	there uses a fresh, valid CASCADE_HOME. Build-tagged !windows to match
//	daemon_unix_test.go: on Windows, daemon_windows.go's platformDaemon*
//	implementations never call loadDaemonConfig at all (they refuse via
//	internal/daemon's Windows build before touching deps), so these
//	assertions about loadDaemonConfig's error text would not hold there.
//
// Inputs: a daemonDeps built directly (never productionDaemonDeps/
//
//	newRootCmd) so each test controls exactly which config.toml content
//	reaches runtime.Load/daemon.ResolveSettings, without ever spawning a
//	real background process (Art.7.1 — deterministic, no real daemon).
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates; R-14.117 sibling
//
//	split — a build-tagged unit-test file for daemon.go's shared plumbing).
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// newTestDaemonDeps builds a daemonDeps rooted at a fresh t.TempDir(),
// writing configContents to that root's config.toml first (skipped when
// configContents is empty, leaving the file absent — Load's documented
// "missing file is not an error" path).
func newTestDaemonDeps(t *testing.T, configContents string) daemonDeps {
	t.Helper()
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if configContents != "" {
		if err := os.WriteFile(paths.ConfigPath(), []byte(configContents), 0o600); err != nil {
			t.Fatalf("write config.toml: %v", err)
		}
	}
	return daemonDeps{
		Paths:      paths,
		Getenv:     func(string) string { return "" },
		Environ:    func() []string { return nil },
		Executable: os.Executable,
	}
}

// TestLoadDaemonConfig_MalformedTOML asserts the runtime.Load failure
// branch: a config.toml that fails to parse surfaces as a KindInvalidInput
// error mentioning "load config.toml", never a panic or a silently zeroed
// Settings.
func TestLoadDaemonConfig_MalformedTOML(t *testing.T) {
	deps := newTestDaemonDeps(t, "this is not [ valid toml")
	_, _, _, err := loadDaemonConfig(context.Background(), deps)
	if err == nil {
		t.Fatal("expected an error for malformed config.toml")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "load config.toml") {
		t.Errorf("err = %v, want mention of \"load config.toml\"", err)
	}
}

// TestLoadDaemonConfig_InvalidShutdownGrace asserts the second failure
// branch: config.toml parses fine but daemon.ResolveSettings rejects an
// unparseable [daemon] shutdown_grace value.
func TestLoadDaemonConfig_InvalidShutdownGrace(t *testing.T) {
	deps := newTestDaemonDeps(t, "[daemon]\nshutdown_grace = \"not-a-duration\"\n")
	_, _, _, err := loadDaemonConfig(context.Background(), deps)
	if err == nil {
		t.Fatal("expected an error for an invalid shutdown_grace value")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "shutdown_grace") {
		t.Errorf("err = %v, want mention of shutdown_grace", err)
	}
}

// TestLoadDaemonConfig_MissingFileIsNotAnError pins the documented
// "missing file is not an error" contract for the success branch this file
// otherwise never exercises directly (daemon_test.go covers it only
// indirectly, through the cobra layer).
func TestLoadDaemonConfig_MissingFileIsNotAnError(t *testing.T) {
	deps := newTestDaemonDeps(t, "")
	cfg, paths, settings, err := loadDaemonConfig(context.Background(), deps)
	if err != nil {
		t.Fatalf("missing config.toml should not error: %v", err)
	}
	if cfg == nil {
		t.Error("cfg = nil, want a resolved default Config")
	}
	if paths == nil {
		t.Error("paths = nil, want the injected PathProvider")
	}
	if settings.SocketPath != paths.SocketPath() {
		t.Errorf("settings.SocketPath = %q, want %q", settings.SocketPath, paths.SocketPath())
	}
}

// execDaemonTree runs cmd (a `daemon` command tree built directly against a
// test daemonDeps, never productionDaemonDeps) with args, bypassing
// newRootCmd entirely so no test here risks the real cascade binary or the
// running test binary itself being relaunched as a background daemon.
func execDaemonTree(t *testing.T, deps daemonDeps, args ...string) error {
	t.Helper()
	cmd := newDaemonCmd(deps)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	return cmd.Execute()
}

// TestDaemonRunCmd_ConfigError proves newDaemonRunCmd's RunE actually
// invokes platformDaemonRun (rather than only being constructed): a
// malformed config.toml makes loadDaemonConfig fail before the foreground
// run loop ever starts, so this returns promptly instead of blocking.
func TestDaemonRunCmd_ConfigError(t *testing.T) {
	deps := newTestDaemonDeps(t, "this is not [ valid toml")
	err := execDaemonTree(t, deps, "run")
	if err == nil {
		t.Fatal("expected an error for malformed config.toml")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestDaemonStartCmd_ConfigError covers newDaemonStartCmd's error-return
// branch: platformDaemonStart fails at loadDaemonConfig, before ever
// resolving deps.Executable() or attempting a spawn.
func TestDaemonStartCmd_ConfigError(t *testing.T) {
	deps := newTestDaemonDeps(t, "this is not [ valid toml")
	err := execDaemonTree(t, deps, "start")
	if err == nil {
		t.Fatal("expected an error for malformed config.toml")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestDaemonRestartCmd_ConfigError is TestDaemonStartCmd_ConfigError's
// counterpart for newDaemonRestartCmd.
func TestDaemonRestartCmd_ConfigError(t *testing.T) {
	deps := newTestDaemonDeps(t, "this is not [ valid toml")
	err := execDaemonTree(t, deps, "restart")
	if err == nil {
		t.Fatal("expected an error for malformed config.toml")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestEnsureSpawnDirs_PropagatesMkdirFailure covers ensureSpawnDirs'
// error-wrapping branch (already partially covered by
// TestEnsureSpawnDirs_CreatesLogDir's success path in daemon_unix_test.go):
// a LogDir() that collides with an existing regular file cannot be
// MkdirAll'd, so ensureSpawnDirs must surface a KindUnavailable error
// rather than the raw *fs.PathError.
func TestEnsureSpawnDirs_PropagatesMkdirFailure(t *testing.T) {
	root := t.TempDir()
	// paths.LogDir() is root/logs; pre-creating "logs" as a plain FILE
	// makes os.MkdirAll(root/logs, ...) fail with ENOTDIR.
	if err := os.WriteFile(filepath.Join(root, "logs"), []byte("x"), 0o600); err != nil {
		t.Fatalf("seed colliding file: %v", err)
	}
	err := ensureSpawnDirs(fakeDaemonPaths{root: root})
	if err == nil {
		t.Fatal("expected an error when LogDir() collides with a regular file")
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err = %v, want KindUnavailable", err)
	}
}
