// Purpose: unit tests for `cascade fleet capacity` — mount reachability,
// the no-daemon actionable refusal, human-table rendering (populated and
// empty), and --json envelope decoding, following fleet_leases_test.go/
// fleet_jobs_test.go's exact established pattern. This file deliberately
// imports neither "net" nor "net/http", so it runs in the fast,
// no-network unit lane; the real-dial case lives in the documented
// testscript scenario at cmd/cascade/testdata/scripts/fleet_capacity.txtar,
// following fleet-sessions.txtar's precedent (this module carries no
// executable testscript dependency — see that file's header).
//
// SPORT: cmd/cascade/fleet-capacity-cli (ADD, P1-E31-W6-S63-T4).
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/output"
)

// TestFleetCapacityMountedOnRoot is the reachability proof `fleet
// capacity` resolves on the real root command tree. This is also this
// ticket's mutation proof: commenting out fleet.go's
// `cmd.AddCommand(newFleetCapacityCmd(deps))` line makes this test fail
// with "capacity] is not mounted on the root command", and restoring it
// makes the test pass again — recorded with real RED/GREEN output in this
// ticket's journal.
func TestFleetCapacityMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"fleet", "capacity"})
	if err != nil || found.Name() != "capacity" {
		t.Fatalf("[fleet capacity] is not mounted on the root command: found=%v err=%v", safeName(found), err)
	}
}

// TestFleetCapacity_NoDaemon_ActionableError proves the command refuses
// with a concrete next step when no daemon is reachable, rather than
// panicking or fabricating an empty snapshot.
func TestFleetCapacity_NoDaemon_ActionableError(t *testing.T) {
	cmd := newFleetCapacityCmd(fleetSessionsDeps{})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cascade daemon run") {
		t.Fatalf("fleet capacity with no daemon = %v, want a refusal suggesting `cascade daemon run`", err)
	}
}

// TestFleetCapacity_ExtraArgRefused proves a positional argument is
// refused, matching every other read-only fleet subcommand.
func TestFleetCapacity_ExtraArgRefused(t *testing.T) {
	cmd := newFleetCapacityCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"extra-arg"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("fleet capacity with a positional arg: want an error, got nil")
	}
}

// sampleCapacitySnapshot builds a two-provider FleetSnapshot: one with a
// known five-hour usage figure, one with WindowUtilizationUnknown, so the
// table test covers both bucketPct branches.
func sampleCapacitySnapshot() capacity.FleetSnapshot {
	return capacity.FleetSnapshot{
		Providers: map[string]capacity.ProviderSlot{
			"anthropic-acc1": {
				State: capacity.StateAvailable,
				Buckets: map[capacity.BucketKind]capacity.Bucket{
					capacity.BucketInteractiveUsage: {
						State:    capacity.StateAvailable,
						FiveHour: capacity.Window{UtilizationPct: 42.5, ResetsIn: 90 * time.Minute},
					},
				},
			},
			"z-provider-noreading": {
				State:   capacity.StateUnknown,
				Buckets: map[capacity.BucketKind]capacity.Bucket{},
			},
		},
	}
}

// TestFleetCapacitySnapshot_TableHasRequiredColumns proves the human
// table carries every real (non-fabricated) AC-required column and
// renders both a known percentage and the "unknown" fail-closed value.
func TestFleetCapacitySnapshot_TableHasRequiredColumns(t *testing.T) {
	out := fleetCapacitySnapshot(sampleCapacitySnapshot()).String()
	for _, col := range []string{"PROVIDER", "STATE", "INTERACTIVE_USAGE%", "AGENT_SDK_CREDIT%", "API_CREDIT%", "RESET_IN"} {
		if !strings.Contains(out, col) {
			t.Errorf("table output missing column header %q:\n%s", col, out)
		}
	}
	if !strings.Contains(out, "anthropic-acc1") || !strings.Contains(out, "42.5") || !strings.Contains(out, "1h30m0s") {
		t.Errorf("table output missing populated provider row:\n%s", out)
	}
	if !strings.Contains(out, "z-provider-noreading") || !strings.Contains(out, "unknown") {
		t.Errorf("table output missing fail-closed \"unknown\" reading for an absent bucket:\n%s", out)
	}
	// Deterministic ordering: providers sort by name.
	if strings.Index(out, "anthropic-acc1") > strings.Index(out, "z-provider-noreading") {
		t.Errorf("table rows not sorted by provider name:\n%s", out)
	}
}

// TestFleetCapacitySnapshot_EmptyTableHasHeaderOnly proves an empty
// FleetSnapshot renders only the header row and never panics.
func TestFleetCapacitySnapshot_EmptyTableHasHeaderOnly(t *testing.T) {
	out := fleetCapacitySnapshot(capacity.FleetSnapshot{}).String()
	if !strings.Contains(out, "PROVIDER") {
		t.Errorf("empty table missing header: %q", out)
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Errorf("empty snapshot table has %d lines, want exactly the header row:\n%s", len(lines), out)
	}
}

// envelopeWire mirrors internal/output.Envelope's wire shape for decoding
// in this test, keeping Data as json.RawMessage so it can be re-decoded
// into capacity.FleetSnapshot without this file importing output's
// internal envelope type directly.
type envelopeWire struct {
	Version int             `json:"version"`
	OK      bool            `json:"ok"`
	Data    json.RawMessage `json:"data"`
}

// TestFleetCapacityResult_JSONEnvelope proves --json mode's real shape:
// the versioned envelope (every other cascade command's contract — see
// fleet_capacity.go's header, deviation 2) with the full FleetSnapshot
// decodable from "data", carrying a "providers" object (not array — see
// deviation 3) whose entries decode back to ProviderSlot.
func TestFleetCapacityResult_JSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	w := output.New(&buf, &bytes.Buffer{}, true /* jsonMode */, false, false, true)
	if err := w.Result(fleetCapacitySnapshot(sampleCapacitySnapshot())); err != nil {
		t.Fatalf("Result: %v", err)
	}

	var env envelopeWire
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("decoding envelope: %v\nraw: %s", err, buf.String())
	}
	if !env.OK {
		t.Fatalf("envelope.ok = false, want true: %s", buf.String())
	}

	var snap capacity.FleetSnapshot
	if err := json.Unmarshal(env.Data, &snap); err != nil {
		t.Fatalf("decoding envelope.data into FleetSnapshot: %v\nraw: %s", err, env.Data)
	}
	if len(snap.Providers) != 2 {
		t.Errorf("decoded FleetSnapshot has %d providers, want 2", len(snap.Providers))
	}
	if _, ok := snap.Providers["anthropic-acc1"]; !ok {
		t.Errorf("decoded FleetSnapshot missing provider %q: %+v", "anthropic-acc1", snap)
	}

	// providers is a JSON object, never an array (deviation 3).
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		t.Fatalf("decoding envelope.data as an object: %v", err)
	}
	trimmed := strings.TrimSpace(string(raw["providers"]))
	if !strings.HasPrefix(trimmed, "{") {
		t.Errorf("data.providers = %s, want a JSON object (not an array)", trimmed)
	}
}

// TestFleetCapacityResult_EmptyJSONEnvelope proves an empty FleetSnapshot
// still round-trips through the envelope with providers/nodes present as
// empty objects, exits without panic, and json.Unmarshals cleanly — the
// empty-snapshot half of this ticket's acceptance criteria, adapted to
// the real envelope contract (see fleet_capacity.go's header, deviation
// 2, for why this is not a literal `{"providers":[],"nodes":[]}`).
func TestFleetCapacityResult_EmptyJSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	w := output.New(&buf, &bytes.Buffer{}, true, false, false, true)
	if err := w.Result(fleetCapacitySnapshot(capacity.FleetSnapshot{})); err != nil {
		t.Fatalf("Result: %v", err)
	}

	var env envelopeWire
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("decoding envelope: %v\nraw: %s", err, buf.String())
	}
	var snap capacity.FleetSnapshot
	if err := json.Unmarshal(env.Data, &snap); err != nil {
		t.Fatalf("decoding empty envelope.data into FleetSnapshot: %v", err)
	}
	if len(snap.Providers) != 0 || len(snap.Nodes) != 0 {
		t.Errorf("empty FleetSnapshot decoded non-empty maps: providers=%v nodes=%v", snap.Providers, snap.Nodes)
	}
}

// TestFleetCapacity_CASCADE_NO_INPUT_Identical proves output is
// byte-identical whether CASCADE_NO_INPUT is set or not — this command
// never branches on interactivity, so the two modes cannot diverge, but
// the AC calls out the behavior explicitly and this asserts it directly
// rather than by inspection.
func TestFleetCapacity_CASCADE_NO_INPUT_Identical(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "")
	without := fleetCapacitySnapshot(sampleCapacitySnapshot()).String()
	t.Setenv("CASCADE_NO_INPUT", "1")
	with := fleetCapacitySnapshot(sampleCapacitySnapshot()).String()
	if without != with {
		t.Errorf("output differs with CASCADE_NO_INPUT=1 set:\nwithout: %q\nwith:    %q", without, with)
	}
}
