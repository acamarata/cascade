//go:build !windows

// Purpose: unit coverage for openBackupRuntime, the backup CLI's own
//
//	thin composition wrapper over the already-well-tested
//	openRuntimeStore (daemon_unix_store_test.go covers that function's
//	migration behavior directly) -- this file shipped with zero direct
//	callers of its own, so the wrapper's field assembly (Store/DB/
//	DataDir/Close) was never exercised.
//
// SPORT: cmd.cascade.backup-store/TEST (P1-E19-W4-S42-T3).
package main

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

func TestOpenBackupRuntime_AssemblesRuntimeFields(t *testing.T) {
	dir := t.TempDir()
	paths := fakeDaemonPaths{root: dir}
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC))

	rt, err := openBackupRuntime(context.Background(), paths, clock)
	if err != nil {
		t.Fatalf("openBackupRuntime: %v", err)
	}
	t.Cleanup(rt.Close)

	if rt.Store == nil {
		t.Error("openBackupRuntime: Store is nil")
	}
	if rt.DB == nil {
		t.Error("openBackupRuntime: DB is nil")
	}
	if rt.DataDir != paths.DataDir() {
		t.Errorf("openBackupRuntime: DataDir = %q, want %q", rt.DataDir, paths.DataDir())
	}
	if rt.Close == nil {
		t.Fatal("openBackupRuntime: Close is nil")
	}
}
