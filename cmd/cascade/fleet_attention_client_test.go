// Purpose: unit coverage for dialFleetAttention's daemonless refusal and
//
//	fleetAttentionOutputWriter's pure flag-to-mode mapping, mirroring
//	dialFleetJobsClient/fleetJournalOutputWriter's own tested pattern for
//	these two sibling functions that shipped with no direct test.
//
// SPORT: cmd.cascade.fleet-attention/TEST (fleet attention composition root).
package main

import (
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestDialFleetAttention_NoDaemonlessStateRefuses(t *testing.T) {
	deps := fleetSessionsDeps{Paths: fakeDaemonPaths{root: t.TempDir()}}
	_, err := dialFleetAttention(context.Background(), deps)
	if err == nil {
		t.Fatal("dialFleetAttention with no DaemonlessState in ctx = nil error, want errAttentionNoDaemon")
	}
}

func TestDialFleetAttention_EmbeddedStateRefuses(t *testing.T) {
	deps := fleetSessionsDeps{Paths: fakeDaemonPaths{root: t.TempDir()}}
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})
	_, err := dialFleetAttention(ctx, deps)
	if err == nil {
		t.Fatal("dialFleetAttention with Embedded=true = nil error, want errAttentionNoDaemon")
	}
}

func TestFleetAttentionOutputWriter_ResolvesFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", true, "")

	w := fleetAttentionOutputWriter(cmd)
	if !w.Mode().JSON {
		t.Error("fleetAttentionOutputWriter: Mode().JSON = false, want true")
	}
	if !w.Mode().NoColor {
		t.Error("fleetAttentionOutputWriter: Mode().NoColor = false, want true")
	}
}
