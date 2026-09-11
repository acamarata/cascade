package inventory

import (
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
)

func TestLoad(t *testing.T) {
	root := &cobra.Command{Use: "cascade"}
	root.AddCommand(&cobra.Command{Use: "sub"})
	clock := runtime.NewFixedClock(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC))

	r, err := Load(root, clock)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if r.ErrorKinds != 14 {
		t.Errorf("ErrorKinds = %d, want 14", r.ErrorKinds)
	}
	if r.StorageDomains != 11 {
		t.Errorf("StorageDomains = %d, want 11", r.StorageDomains)
	}
	if r.CLICommands != 2 {
		t.Errorf("CLICommands = %d, want 2", r.CLICommands)
	}
	if r.GeneratedAt != "2026-03-04T05:06:07Z" {
		t.Errorf("GeneratedAt = %q, want 2026-03-04T05:06:07Z", r.GeneratedAt)
	}
	// Providers/Plugins/SPORTLines/Platforms come from the real, tracked
	// counts.json this repo ships — assert only that they were populated
	// (non-negative, Platforms non-empty), never a specific number, since
	// this test must not itself become a second hand-maintained count that
	// drifts from the generator's own output.
	if r.Providers < 0 || r.Plugins < 0 || r.SPORTLines < 0 {
		t.Errorf("Load: negative generated field in %+v", r)
	}
	if len(r.Platforms) == 0 {
		t.Errorf("Load: Platforms is empty, want the embedded counts.json's list")
	}
}
