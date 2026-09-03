//go:build !windows

// Purpose: the end-to-end proof RecoveryRegistry needs: orphaned
//
//	advisory-lock cleanup running through platformDaemonRun, the real
//	production entry point, with a StoreDomainRegistry platformDaemonRun
//	builds internally rather than one this test injects. The only thing
//	this test constructs itself is the seed data (a stale lock record)
//	and the dead pid proving it, via the same openRuntimeStore helper
//	production uses, at the same path production would use.
//
// SPORT: cmd/cascade/daemon (ADD).
package main

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestWireUpgrade_DevBuild_LeavesUpgradeNil is a regression test for the
// self-relaunch-loop bug the SIGTERM/SIGKILL round-trip test
// (TestDaemonStartStopRestartStatus_RealBinary, daemon_test.go) caught
// during development: daemon.BuildHash() is "dev" for every unreleased
// build (which is every build this repo's own CI and test suite ever
// run), and CheckSkew reports skew against a "dev" build unconditionally.
// Wiring RunOptions.Upgrade without this guard means every ordinary
// termination signal on a dev build attempts drain-and-exec-relaunch
// instead of a clean exit, which is the daemon-never-stops failure that
// test exercises end to end. This test pins the guard directly: on the
// dev build this test itself runs as, wireUpgrade must leave every field
// it would otherwise set at its zero value.
func TestWireUpgrade_DevBuild_LeavesUpgradeNil(t *testing.T) {
	if daemon.BuildHash() != "dev" {
		t.Skip("this binary carries a real release build hash; the guard this test pins does not apply")
	}
	var opts daemon.RunOptions
	deps := newRunTestDeps(t, func() (string, error) { return "/bin/true", nil })
	bus := events.New(storetest.NewMemStore(), deps.Clock)
	wireUpgrade(&opts, deps, storetest.NewMemStore(), bus, nil)

	if opts.Upgrade != nil {
		t.Error("wireUpgrade set Upgrade on a dev build, want nil")
	}
	if opts.Executable != nil {
		t.Error("wireUpgrade set Executable on a dev build, want nil")
	}
	if opts.Args != nil {
		t.Error("wireUpgrade set Args on a dev build, want nil")
	}
}

// deadPid spawns a real short-lived child process, waits for it to exit
// and be reaped, and returns its pid: a pid the OS unambiguously reports
// not-alive for immediately afterward, a real counterpart rather than a
// guessed-at number.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn+run short-lived child: %v", err)
	}
	return cmd.Process.Pid
}

// TestPlatformDaemonRun_OrphanedAdvisoryLock_CleanedUpByProductionPath
// seeds a stale lock (owned by a confirmed-dead pid) into the exact
// on-disk store platformDaemonRun itself opens, runs platformDaemonRun
// for real, and then re-opens that same store to prove the lock is gone.
// platformDaemonRun builds its own runtime.NewStoreDomainRegistry
// internally (daemon_unix.go); this test never constructs one and passes
// it in, so a passing test proves reachability from the real entry point,
// not a component test's ability to drive DomainRegistry directly.
func TestPlatformDaemonRun_OrphanedAdvisoryLock_CleanedUpByProductionPath(t *testing.T) {
	deps := newRunTestDeps(t, nil)
	paths := deps.Paths

	pid := deadPid(t)
	seedStore, closeSeed, err := openRuntimeStore(context.Background(), paths)
	if err != nil {
		t.Fatalf("openRuntimeStore (seed): %v", err)
	}
	if err := runtime.NewStoreDomainRegistry(seedStore).RegisterLock(context.Background(), "e2e-stale-lock", pid); err != nil {
		t.Fatalf("RegisterLock (seed): %v", err)
	}
	closeSeed() // release the flock before platformDaemonRun opens the same file

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := platformDaemonRun(ctx, deps); err != nil {
		t.Fatalf("platformDaemonRun: %v", err)
	}

	verifyStore, closeVerify, err := openRuntimeStore(context.Background(), paths)
	if err != nil {
		t.Fatalf("openRuntimeStore (verify): %v", err)
	}
	defer closeVerify()
	locks, err := runtime.NewStoreDomainRegistry(verifyStore).OrphanedLocks(context.Background())
	if err != nil {
		t.Fatalf("OrphanedLocks (verify): %v", err)
	}
	for _, l := range locks {
		if l.LockID == "e2e-stale-lock" {
			t.Fatalf("lock %+v still present after platformDaemonRun; production recovery path did not run", l)
		}
	}
}
