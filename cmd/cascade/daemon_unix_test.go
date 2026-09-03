//go:build !windows

// Purpose: unit tests for daemon_unix.go's small pure/near-pure helpers
//
//	(relaunchArgs, startDetail, stopDetail, socketDialable, ensureSpawnDirs)
//	that the real-binary end-to-end test in daemon_test.go exercises
//	behaviorally but does not instrument for `go test -cover` (it runs a
//	SEPARATE built binary via exec.Command, so go tool cover attributes
//	none of that execution to this test process) — these direct-call unit
//	tests close that measurement gap for Art.4's CLI coverage floor.
//
// SPORT: cmd/cascade/daemon (ADD, per T-2 sport_updates; R-14.117 sibling
//
//	split — a build-tagged unit-test file for daemon_unix.go's helpers).
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestRelaunchArgs(t *testing.T) {
	orig := globalFlags
	defer func() { globalFlags = orig }()

	globalFlags = GlobalFlags{}
	if got := relaunchArgs(); len(got) != 2 || got[0] != "daemon" || got[1] != "run" {
		t.Errorf("relaunchArgs() = %v, want [daemon run]", got)
	}

	globalFlags = GlobalFlags{Config: "/tmp/x.toml", Profile: "server"}
	got := relaunchArgs()
	want := []string{"daemon", "run", "--config", "/tmp/x.toml", "--profile", "server"}
	if len(got) != len(want) {
		t.Fatalf("relaunchArgs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("relaunchArgs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStartDetail(t *testing.T) {
	if got := startDetail(daemon.StartResult{AlreadyRunning: true, PID: 42}); got != "already running pid=42" {
		t.Errorf("got %q", got)
	}
	if got := startDetail(daemon.StartResult{PID: 7}); got != "started pid=7" {
		t.Errorf("got %q", got)
	}
}

func TestStopDetail(t *testing.T) {
	cases := []struct {
		res  daemon.StopResult
		want string
	}{
		{daemon.StopResult{WasRunning: false}, "not running"},
		{daemon.StopResult{WasRunning: true, Escalated: true}, "stopped (escalated to SIGKILL)"},
		{daemon.StopResult{WasRunning: true}, "stopped"},
	}
	for _, c := range cases {
		if got := stopDetail(c.res); got != c.want {
			t.Errorf("stopDetail(%+v) = %q, want %q", c.res, got, c.want)
		}
	}
}

func TestSocketDialable(t *testing.T) {
	if socketDialable(filepath.Join(t.TempDir(), "nothing.sock")) {
		t.Error("socketDialable = true against a socket path nothing is listening on")
	}
}

func TestEnsureSpawnDirs_CreatesLogDir(t *testing.T) {
	root := t.TempDir()
	paths := fakeDaemonPaths{root: root}
	if err := ensureSpawnDirs(paths); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(paths.LogDir()); err != nil || !info.IsDir() {
		t.Errorf("LogDir() not created: err=%v", err)
	}
}

// fakeDaemonPaths is a minimal runtime.PathProvider for this file's tests.
type fakeDaemonPaths struct{ root string }

func (p fakeDaemonPaths) Root() string       { return p.root }
func (p fakeDaemonPaths) ConfigPath() string { return filepath.Join(p.root, "config.toml") }
func (p fakeDaemonPaths) SocketPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p fakeDaemonPaths) DataDir() string    { return filepath.Join(p.root, "data") }
func (p fakeDaemonPaths) LogDir() string     { return filepath.Join(p.root, "logs") }
func (p fakeDaemonPaths) StorageRoot(prof runtime.Profile) string {
	return filepath.Join(p.root, "data", "storage", string(prof))
}
