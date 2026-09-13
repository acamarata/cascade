// Purpose: unit coverage for dialFleetJobsClient's refusal and success
//
//	branches, which shipped with no direct test of its own despite being
//	referenced as an already-tested precedent in sibling comments.
//
// SPORT: cmd.cascade.fleet-jobs/TEST (fleet jobs client composition root).
package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestDialFleetJobsClient_NoDaemonlessStateRefuses(t *testing.T) {
	deps := fleetSessionsDeps{Paths: fakeDaemonPaths{root: t.TempDir()}}
	_, err := dialFleetJobsClient(context.Background(), deps)
	if err == nil {
		t.Fatal("dialFleetJobsClient with no DaemonlessState in ctx = nil error, want errFleetJobsNoDaemon")
	}
}

func TestDialFleetJobsClient_LiveDaemonBuildsClient(t *testing.T) {
	deps := fleetSessionsDeps{Paths: fakeDaemonPaths{root: t.TempDir()}, DialContext: client.UnixDialer}
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	c, err := dialFleetJobsClient(ctx, deps)
	if err != nil {
		t.Fatalf("dialFleetJobsClient: %v", err)
	}
	if c == nil {
		t.Error("dialFleetJobsClient: client is nil")
	}
}
