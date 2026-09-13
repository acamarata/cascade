// Purpose: unit coverage for fleetTopClient's daemonless refusal branch
//
//	(fleet_top.go), mirroring dialFleetJobsClient's own tested refusal
//	pattern for the sibling `fleet top` client this file's own doc
//	comment says mirrors it exactly.
//
// SPORT: cmd.cascade.fleet-top/TEST (fleet top composition root).
package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestFleetTopClient_NoDaemonlessStateRefuses(t *testing.T) {
	deps := fleetSessionsDeps{Paths: fakeDaemonPaths{root: t.TempDir()}}
	_, err := fleetTopClient(context.Background(), deps)
	if err == nil {
		t.Fatal("fleetTopClient with no DaemonlessState in ctx = nil error, want errFleetTopNoDaemon")
	}
}

func TestFleetTopClient_EmbeddedStateRefuses(t *testing.T) {
	deps := fleetSessionsDeps{Paths: fakeDaemonPaths{root: t.TempDir()}}
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})
	_, err := fleetTopClient(ctx, deps)
	if err == nil {
		t.Fatal("fleetTopClient with Embedded=true = nil error, want errFleetTopNoDaemon")
	}
}
