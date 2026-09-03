//go:build !windows

// Purpose: coverage for daemon_unix.go's platformDaemonStatus error branch
//
//	daemon_unix_errors_test.go does not cover (daemon.Status()'s own
//	failure, as opposed to loadDaemonConfig's, which that file's
//	TestPlatformDaemonStatus_ConfigError already closes). socketDialable's
//	success path (something IS listening) needs a real net.Listener, which
//	Art.7.2's no-network-unit-lane gate forbids importing "net" for outside
//	an integration-tagged file — that case now lives in
//	daemon_unix_socket_integration_test.go (`-tags=integration`), the same
//	split internal/daemon's own daemon_unix_integration_test.go already
//	uses for exactly this reason (T0 fix, coverage-gate task: this file
//	previously imported "net" directly and tripped the hygiene gate).
//
// Constraints: no test here spawns a real daemon process; the corrupt-
//
//	pidfile case writes a plain file under t.TempDir().
//
// SPORT: cmd/cascade/daemon (coverage-only addition, no sport_updates —
//
//	this file adds no new production surface).
package main

import (
	"context"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestPlatformDaemonStatus_CorruptPIDFile_Error covers daemon.Status()'s
// own error branch: a pidfile that exists but fails to parse as JSON is a
// typed KindIntegrity error, never silently reported as "not running".
func TestPlatformDaemonStatus_CorruptPIDFile_Error(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	pidPath := daemon.PIDFilePath(paths)
	if err := os.WriteFile(pidPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt pidfile: %v", err)
	}
	deps := daemonDeps{
		Paths:   paths,
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Clock:   runtime.SystemClock{},
	}
	_, err := platformDaemonStatus(context.Background(), deps)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("err = %v, want KindIntegrity from the corrupt pidfile", err)
	}
}
