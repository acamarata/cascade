//go:build windows

// Purpose: the Windows mirror of daemon_status_stop_unix_test.go
//
//	(Windows parity pass 3): internal/daemon's Windows build
//	(lifecycle_windows.go) refuses Status and Stop unconditionally with
//	cascade.KindUnsupported, so "nothing running" here is a typed
//	refusal, never the JSON envelope / idempotent-no-op shape the unix
//	mirror asserts for the platforms with a real daemon.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates), windows-parity-pass-3.
package main

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestDaemonStatusCmd_RefusesOnWindows(t *testing.T) {
	home := t.TempDir()
	out, err := execDaemon(t, home, "daemon", "status", "--json")
	if err == nil {
		t.Fatalf("daemon status succeeded on Windows, where no daemon exists: %s", out)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("refusal kind = %v, want unsupported", err)
	}
	if !strings.Contains(err.Error(), "not supported on this platform") {
		t.Errorf("refusal does not name the platform limitation: %v", err)
	}
}

func TestDaemonStopCmd_RefusesOnWindows(t *testing.T) {
	home := t.TempDir()
	out, err := execDaemon(t, home, "daemon", "stop")
	if err == nil {
		t.Fatalf("daemon stop succeeded on Windows, where no daemon exists: %s", out)
	}
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Errorf("refusal kind = %v, want unsupported", err)
	}
	if !strings.Contains(err.Error(), "no daemon on Windows") {
		t.Errorf("refusal does not name the platform limitation: %v", err)
	}
}
