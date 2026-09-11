// Purpose: tests for `cascade doctor sport` — the reachability proof (and
//
//	the proof that removing it makes the reachability test fail), the
//	human and --json renders, and the filter flags.
//
// SPORT: cmd/cascade/doctor-sport/ADDED (tests).
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/inventory/sport"
)

// TestDoctorSportIsMountedOnRoot proves `doctor sport` resolves under the
// mounted doctor command on the REAL root tree. Removing
// `cmd.AddCommand(newDoctorSportCmd(deps))` from newDoctorCmd (doctor.go)
// makes this fail with "unknown command \"sport\"" — see
// TestDoctorSportWiringCanFail below for the proof that this specific
// assertion is the one that would catch it.
func TestDoctorSportIsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"doctor", "sport"})
	if err != nil || found.Name() != "sport" {
		t.Fatalf("doctor sport is not mounted: found=%v err=%v", found.Name(), err)
	}
}

// TestDoctorSportWiringCanFail proves TestDoctorSportIsMountedOnRoot
// above actually has teeth: a doctor command tree built with every OTHER
// real mount call (bundle, counts) but WITHOUT
// `cmd.AddCommand(newDoctorSportCmd(deps))` — simulating that one line
// being removed from newDoctorCmd — fails Find(["sport"]) the same way
// the real mount's removal would.
func TestDoctorSportWiringCanFail(t *testing.T) {
	deps := testDoctorDeps(t)
	unwired := &cobra.Command{Use: "doctor"}
	unwired.AddCommand(newDoctorBundleCmd(deps))
	unwired.AddCommand(newDoctorCountsCmd(deps))
	if _, _, err := unwired.Find([]string{"sport"}); err == nil {
		t.Fatal("expected Find to fail when doctor sport is not mounted, it did not")
	}
}

// TestDoctorSport_HumanOutput drives `doctor sport` end to end and asserts
// the human table contains the summary line's field names.
func TestDoctorSport_HumanOutput(t *testing.T) {
	deps := testDoctorDeps(t)
	out, err := execRootDoctor(t, deps, "doctor", "sport")
	if err != nil {
		t.Fatalf("doctor sport: %v\noutput:\n%s", err, out)
	}
	for _, want := range []string{"sport registry", "entities", "sites"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor sport output missing %q\noutput:\n%s", want, out)
		}
	}
}

// TestDoctorSport_JSONOutput drives `doctor sport --json` and decodes the
// versioned envelope, asserting the entity count agrees with a direct
// sport.LoadRegistry call.
func TestDoctorSport_JSONOutput(t *testing.T) {
	deps := testDoctorDeps(t)
	out, err := execRootDoctor(t, deps, "doctor", "sport", "--json")
	if err != nil {
		t.Fatalf("doctor sport --json: %v\noutput:\n%s", err, out)
	}
	var envelope struct {
		OK   bool           `json:"ok"`
		Data sport.Registry `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode envelope: %v\noutput:\n%s", err, out)
	}
	if !envelope.OK {
		t.Fatalf("envelope.OK = false, output:\n%s", out)
	}
	want, err := sport.LoadRegistry()
	if err != nil {
		t.Fatalf("sport.LoadRegistry: %v", err)
	}
	if len(envelope.Data.Entities) != len(want.Entities) {
		t.Errorf("Entities = %d, want %d", len(envelope.Data.Entities), len(want.Entities))
	}
}

// TestDoctorSport_StatusFilter proves --status narrows the entity set to
// only entities carrying that status.
func TestDoctorSport_StatusFilter(t *testing.T) {
	deps := testDoctorDeps(t)
	out, err := execRootDoctor(t, deps, "doctor", "sport", "--json", "--status", "CHANGE")
	if err != nil {
		t.Fatalf("doctor sport --status CHANGE: %v\noutput:\n%s", err, out)
	}
	var envelope struct {
		Data sport.Registry `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode envelope: %v\noutput:\n%s", err, out)
	}
	if len(envelope.Data.Entities) == 0 {
		t.Fatal("expected at least one entity carrying a CHANGE status")
	}
	for _, e := range envelope.Data.Entities {
		found := false
		for _, s := range e.Statuses {
			if s == "CHANGE" {
				found = true
			}
		}
		if !found {
			t.Errorf("entity %q returned by --status CHANGE carries no CHANGE status: %v", e.Name, e.Statuses)
		}
	}
}

// TestDoctorSport_NameFilter proves --name narrows to a substring match.
func TestDoctorSport_NameFilter(t *testing.T) {
	deps := testDoctorDeps(t)
	out, err := execRootDoctor(t, deps, "doctor", "sport", "--json", "--name", "cmd/cascade/daemon")
	if err != nil {
		t.Fatalf("doctor sport --name: %v\noutput:\n%s", err, out)
	}
	var envelope struct {
		Data sport.Registry `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("decode envelope: %v\noutput:\n%s", err, out)
	}
	if len(envelope.Data.Entities) == 0 {
		t.Fatal("expected at least one entity matching cmd/cascade/daemon")
	}
	for _, e := range envelope.Data.Entities {
		if !strings.Contains(e.Name, "cmd/cascade/daemon") {
			t.Errorf("entity %q does not contain the --name filter substring", e.Name)
		}
	}
}
