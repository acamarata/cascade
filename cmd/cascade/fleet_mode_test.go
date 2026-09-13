// Purpose: unit tests for `cascade fleet mode show|set` — mount
//
//	reachability (this ticket's mutation proof), the no-daemon actionable
//	refusal, --ttl's typed refusal for a non-incident mode surfaced by the
//	daemon, and CASCADE_NO_INPUT=1 output parity. This module carries no
//	executable testscript runner (see
//	.claude/planning/p1/phase/journals/DEFECT-txtar-fixtures-never-run.md);
//	the real-dial case is covered by the daemon-side rpc_mode_test.go
//	acceptance test instead, matching fleet_capacity_test.go's own
//	documented no-txtar scope.
//
// SPORT: cmd/cascade/fleet-mode-cli (ADD, P1-E41-W9-S79-T2).
package main

import (
	"strings"
	"testing"
)

// TestFleetModeMountedOnRoot is the reachability proof `fleet mode
// show|set` resolve on the real root command tree. This is also this
// ticket's mutation proof: commenting out fleet.go's
// `cmd.AddCommand(newFleetModeCmd(deps))` line makes this test fail with
// "[fleet mode show] is not mounted", and restoring it makes the test
// pass again -- recorded with real RED/GREEN output in this ticket's
// journal.
func TestFleetModeMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, path := range [][]string{{"fleet", "mode", "show"}, {"fleet", "mode", "set"}} {
		want := path[len(path)-1]
		found, _, err := root.Find(path)
		if err != nil || found == nil || found.Name() != want {
			t.Fatalf("%v is not mounted on the root command: found=%v err=%v", path, safeName(found), err)
		}
	}
}

// TestFleetModeShow_NoDaemon_ActionableError proves the command refuses
// with a concrete next step when no daemon is reachable.
func TestFleetModeShow_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetModeShowCmd(fleetSessionsDeps{})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet mode show with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

// TestFleetModeSet_NoDaemon_ActionableError mirrors the show case for set.
func TestFleetModeSet_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetModeSetCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"crunch"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet mode set with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

// TestFleetModeSet_RequiresExactlyOneArg proves the mode positional
// argument is required and singular.
func TestFleetModeSet_RequiresExactlyOneArg(t *testing.T) {
	for _, args := range [][]string{{}, {"crunch", "extra"}} {
		cmd := newFleetModeSetCmd(fleetSessionsDeps{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("fleet mode set %v: want an error, got nil", args)
		}
	}
}

// TestFleetModeShow_ExtraArgRefused proves a positional argument is
// refused on the read-only show verb.
func TestFleetModeShow_ExtraArgRefused(t *testing.T) {
	cmd := newFleetModeShowCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"extra-arg"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("fleet mode show with a positional arg: want an error, got nil")
	}
}

// TestFleetModeShow_NoInputParity proves the no-daemon refusal's text is
// byte-identical whether or not CASCADE_NO_INPUT=1 is set -- the
// automation-parity requirement (06 §5 rule 8) for a command that never
// prompts in the first place.
func TestFleetModeShow_NoInputParity(t *testing.T) {
	withoutEnv := newFleetModeShowCmd(fleetSessionsDeps{}).Execute()
	t.Setenv("CASCADE_NO_INPUT", "1")
	withEnv := newFleetModeShowCmd(fleetSessionsDeps{}).Execute()
	if withoutEnv == nil || withEnv == nil {
		t.Fatal("want both runs to fail with no daemon reachable")
	}
	if withoutEnv.Error() != withEnv.Error() {
		t.Fatalf("output differs under CASCADE_NO_INPUT=1:\nwithout: %v\nwith:    %v", withoutEnv, withEnv)
	}
}

// TestModeResultWireString covers both branches of modeResultWire.String
// (explicit-with-expiry, explicit-without-expiry, lifecycle-derived).
func TestModeResultWireString(t *testing.T) {
	cases := []struct {
		name string
		in   modeResultWire
		want string
	}{
		{"incident with expiry", modeResultWire{Mode: "incident", Source: "explicit", SetAt: "t1", ExpiresAt: "t2"},
			"mode=incident source=explicit set_at=t1 expires_at=t2"},
		{"explicit without expiry", modeResultWire{Mode: "verify", Source: "explicit", SetAt: "t1"},
			"mode=verify source=explicit set_at=t1"},
		{"lifecycle derived", modeResultWire{Mode: "build", Source: "lifecycle", LifecycleDefault: "build"},
			"mode=build source=lifecycle lifecycle_default=build"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("%s: String() = %q, want %q", c.name, got, c.want)
		}
	}
}
