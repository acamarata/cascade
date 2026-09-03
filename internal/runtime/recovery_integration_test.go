//go:build integration

// Purpose: proves the PRODUCTION defaultDialer (recovery.go) — a real
//   net.DialTimeout call — against a genuine unix socket end-to-end
//   (Art.2's real-counterpart requirement). recovery_test.go's unit-lane
//   tests exercise Scan's branching logic entirely through the injected
//   Dialer fake (Art.7.2 forbids a "net" import in the default,
//   untagged lane); this file is the real-network counterpart, split
//   into its own `//go:build integration` file under R-14.133 (a
//   build-tag file split is forced into existence exactly like that
//   ruling's own `//go:build integration` case and joins the ticket's
//   authorized write set automatically). Run via
//   `go test -tags integration ./internal/runtime/...`.
// Constraints: Art.7.1 — every path is under t.TempDir(). Reuses
//   testLogger/deadPid/intToBytes from recovery_test.go (both files
//   compile together under -tags integration).

package runtime

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortIntegrationTempDir returns a fresh, empty, automatically-cleaned
// temp directory under a short, fixed prefix rather than one that embeds
// the (potentially long) test name — sockaddr_un's sun_path is capped at
// 104 bytes on darwin (108 on linux), and t.TempDir()'s own path embeds
// os.TempDir() (already ~50 chars on macOS) plus the full test name plus
// a numeric suffix, which overflows the limit for a real net.Listen call
// here. Same isolation/cleanup guarantee as t.TempDir() (t.Cleanup +
// RemoveAll); see lockfile_test.go/recovery_test.go's history for the
// same constraint hit and fixed the same way.
func shortIntegrationTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cascaderecit")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestRecoveryScan_RealSocketLiveDaemon proves the real defaultDialer
// correctly detects a genuinely live listener and aborts with no
// cleanup — the same assertion TestRecoveryScanLiveDaemonBlocked makes
// with a fake Dialer, now against actual net.Listen/net.DialTimeout.
func TestRecoveryScan_RealSocketLiveDaemon(t *testing.T) {
	dir := shortIntegrationTempDir(t)
	sockPath := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	ev, scanErr := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(1, 0)),
		Log:         testLogger(&bytes.Buffer{}),
		// Dial intentionally left nil: exercises the real defaultDialer.
	})
	if scanErr == nil {
		t.Fatal("Scan against a real live socket: want error, got nil")
	}
	if !errors.Is(scanErr, ErrDaemonAlreadyRunning) {
		t.Fatalf("Scan error = %v, want errors.Is(err, ErrDaemonAlreadyRunning)", scanErr)
	}
	if ev != nil {
		t.Fatalf("Scan against a real live socket: got event %+v, want nil", ev)
	}
}

// TestRecoveryScan_RealSocketStale proves the real defaultDialer
// correctly classifies a real, but abandoned, unix socket file (nothing
// listening — a genuine ECONNREFUSED from the kernel, not a simulated
// one) as confirmed-stale and removes it.
func TestRecoveryScan_RealSocketStale(t *testing.T) {
	dir := shortIntegrationTempDir(t)
	sockPath := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	if unixLn, ok := ln.(*net.UnixListener); ok {
		unixLn.SetUnlinkOnClose(false)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket file missing immediately after simulated crash: %v", err)
	}

	ev, scanErr := Scan(context.Background(), RecoveryOptions{
		PidfilePath: filepath.Join(dir, "daemon.pid"),
		SocketPath:  sockPath,
		Clock:       NewFixedClock(time.Unix(2, 0)),
		Log:         testLogger(&bytes.Buffer{}),
	})
	if scanErr != nil {
		t.Fatalf("Scan: %v", scanErr)
	}
	if ev == nil || !ev.StaleSocket {
		t.Fatalf("Scan: got %+v, want StaleSocket=true", ev)
	}
	if _, statErr := os.Stat(sockPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal("real stale socket file still present after cleanup")
	}
}
