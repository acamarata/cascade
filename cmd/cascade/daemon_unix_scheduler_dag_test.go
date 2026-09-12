//go:build !windows

// Purpose: proves wireJobScheduler (daemon_unix_scheduler_dag.go) actually
//
//	runs from buildRPCServer -- the SAME composition root
//	platformDaemonRun calls in production -- so AC/S-59.T5's
//	jobs.Scheduler is constructed and reachable from the real daemon
//	entry point, not merely from a test calling jobs.NewScheduler
//	directly. Mirrors TestBuildRPCServer_ConductorExpandReachableOverRealSocket's
//	own "never call the registrar directly" posture.
//
// SPORT: cmd/cascade/daemon (ADD, merge-fix for P1-E29-W6-S59-T5).
package main

import (
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// TestBuildRPCServer_MountsJobScheduler is the mutation-tested proof: with
// wireJobScheduler's call in buildRPCServer present, this test is GREEN.
// Commenting out that call (the mutation this ticket's journal records)
// turns it RED with "manifest has no %q entry" -- the real daemon entry
// point no longer constructs the scheduler, exactly the FAILURE 1 defect
// this file's sibling ticket fixed.
func TestBuildRPCServer_MountsJobScheduler(t *testing.T) {
	clock := runtime.NewSystemClock()
	bus := events.New(storetest.NewMemStore(), clock)
	paths := fakeMemoryPaths{root: t.TempDir()}

	srv, manifest, _, err := buildRPCServer(bus, clock, nil, daemon.Settings{SocketPath: filepath.Join(paths.root, "d.sock")}, paths, nil, nil)
	if err != nil {
		t.Fatalf("buildRPCServer: %v", err)
	}
	if srv == nil {
		t.Fatal("buildRPCServer returned a nil server")
	}
	if manifest == nil {
		t.Fatal("buildRPCServer returned a nil manifest")
	}

	found := false
	for _, s := range manifest.Snapshot() {
		if s.Name == "jobs.scheduler" {
			found = true
			if s.State != daemon.SubsystemRunning {
				t.Fatalf("jobs.scheduler subsystem state = %v, want Running", s.State)
			}
		}
	}
	if !found {
		t.Fatalf("manifest has no %q entry: buildRPCServer, the real daemon composition root, never mounted the DAG scheduler", "jobs.scheduler")
	}
}
