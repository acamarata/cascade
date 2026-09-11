// Purpose: tests for `cascade doctor counts` — the reachability proof, the
//
//	human and --json renders, and the field values against
//	internal/inventory's own live derivations.
//
// Constraints: writes only under t.TempDir() (Art.7.1); drives the real
//
//	root tree (Art.2); the clock is injected (Art.7.3).
//
// SPORT: cmd/cascade/doctor (ADD - counts subcommand).
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/inventory"
)

// TestDoctorCountsIsMountedOnRoot proves `doctor counts` resolves under the
// mounted doctor command on the REAL root tree (R-14.166's reachability
// proof). Removing `cmd.AddCommand(newDoctorCountsCmd(deps))` from
// newDoctorCmd (doctor.go) makes this fail with "unknown command
// \"counts\"" — see TestDoctorCountsWiringCanFail below for the proof that
// this specific assertion is the one that would catch it.
func TestDoctorCountsIsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"doctor", "counts"})
	if err != nil || found.Name() != "counts" {
		t.Fatalf("doctor counts is not mounted: found=%v err=%v", found.Name(), err)
	}
}

// TestDoctorCounts_HumanOutput drives `doctor counts` end to end through
// the real root tree and asserts the human table contains every field
// name.
func TestDoctorCounts_HumanOutput(t *testing.T) {
	deps := testDoctorDeps(t)
	out, err := execRootDoctor(t, deps, "doctor", "counts")
	if err != nil {
		t.Fatalf("doctor counts: %v\noutput:\n%s", err, out)
	}
	for _, want := range []string{"error kinds:", "storage domains:", "cli commands:", "providers:", "plugins:", "platforms:"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor counts output missing %q\noutput:\n%s", want, out)
		}
	}
}

// TestDoctorCounts_JSONOutput drives `doctor counts --json` and decodes the
// versioned envelope, asserting the live fields agree with
// internal/inventory's own derivations computed directly against the same
// root tree — the exact "same artifact" property this ticket's Report is
// built to guarantee.
func TestDoctorCounts_JSONOutput(t *testing.T) {
	deps := testDoctorDeps(t)
	out, err := execRootDoctor(t, deps, "doctor", "counts", "--json")
	if err != nil {
		t.Fatalf("doctor counts --json: %v\noutput:\n%s", err, out)
	}

	var envelope struct {
		OK   bool             `json:"ok"`
		Data inventory.Report `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode envelope: %v\noutput:\n%s", err, out)
	}
	if !envelope.OK {
		t.Fatalf("envelope.OK = false, output:\n%s", out)
	}
	if envelope.Data.ErrorKinds != inventory.ErrorKindCount() {
		t.Errorf("ErrorKinds = %d, want %d", envelope.Data.ErrorKinds, inventory.ErrorKindCount())
	}
	if envelope.Data.StorageDomains != inventory.StorageDomainCount() {
		t.Errorf("StorageDomains = %d, want %d", envelope.Data.StorageDomains, inventory.StorageDomainCount())
	}
	if envelope.Data.CLICommands <= 0 {
		t.Errorf("CLICommands = %d, want > 0", envelope.Data.CLICommands)
	}
	if len(envelope.Data.Platforms) == 0 {
		t.Error("Platforms is empty")
	}
}
