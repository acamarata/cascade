// Purpose: unit tests for `cascade fleet sessions`'s pure logic - table
//
//	rendering, SSE data-block decoding, elapsed formatting, mount
//	reachability, and the hidden `cascade sessions` alias - that need no
//	real socket. This file deliberately imports neither "net" nor
//	"net/http" so it runs in the fast, no-network unit lane
//	(internal/build's no-network-unit-lane gate, Art.7.2); the real-dial
//	end-to-end cases (daemon-unreachable refusal, --watch NDJSON stream,
//	Windows tier-2 refusal, alias resolution through the compiled
//	binary) live in the testscript at
//	cmd/cascade/testdata/scripts/fleet-sessions.txtar (R-40.X17), which
//	execs the real binary rather than importing net from this package.
//
// SPORT: cmd/cascade/fleet (ADD, per T-2 sport_updates).
package main

import (
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestFleetSessionsMountedOnRoot is the R-14.166 reachability proof: both
// `fleet sessions` and the hidden `sessions` alias resolve on the real
// root command tree.
func TestFleetSessionsMountedOnRoot(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, path := range [][]string{{"fleet", "sessions"}, {"sessions"}} {
		found, _, err := root.Find(path)
		if err != nil || found.Name() != "sessions" {
			t.Fatalf("%v is not mounted on the root command: found=%v err=%v", path, safeName(found), err)
		}
	}
}

// TestFleetSessionsAliasHidden proves the top-level `sessions` alias is
// hidden from --help while still resolving.
func TestFleetSessionsAliasHidden(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	found, _, err := root.Find([]string{"sessions"})
	if err != nil {
		t.Fatalf("sessions alias not found: %v", err)
	}
	if !found.Hidden {
		t.Fatal("cascade sessions alias must be Hidden (07 §fleet consolidation note)")
	}
}

// TestFleetSessionRows_TableHasRequiredColumns proves the human table
// carries every AC-required column.
func TestFleetSessionRows_TableHasRequiredColumns(t *testing.T) {
	rows := fleetSessionRows{{
		SessionID: "s1", Binary: "claude", State: "active",
		Confidence: "0.90", Account: "acc1", Elapsed: "5s",
	}}
	out := rows.String()
	for _, col := range []string{"SESSION_ID", "BINARY", "STATE", "CONFIDENCE", "ACCOUNT", "ELAPSED"} {
		if !strings.Contains(out, col) {
			t.Errorf("table output missing column header %q:\n%s", col, out)
		}
	}
	for _, val := range []string{"s1", "claude", "active", "0.90", "acc1", "5s"} {
		if !strings.Contains(out, val) {
			t.Errorf("table output missing value %q:\n%s", val, out)
		}
	}
}

// TestFleetSessionRows_EmptyTableHasHeaderOnly proves an empty result
// still renders the header row, never panics.
func TestFleetSessionRows_EmptyTableHasHeaderOnly(t *testing.T) {
	out := fleetSessionRows{}.String()
	if !strings.Contains(out, "SESSION_ID") {
		t.Errorf("empty table missing header: %q", out)
	}
}

// TestFormatElapsed_ZeroOrNegativeIsDash proves formatElapsed fails
// closed on an absent timestamp rather than printing a bogus duration.
func TestFormatElapsed_ZeroOrNegativeIsDash(t *testing.T) {
	for _, ts := range []int64{0, -1} {
		if got := formatElapsed(ts, runtime.NewSystemClock()); got != "-" {
			t.Errorf("formatElapsed(%d) = %q, want %q", ts, got, "-")
		}
	}
}

// TestDecodeSessionEvent_RoundTrip proves an SSE data block carrying a
// marshaled SessionRecord decodes back to the same record.
func TestDecodeSessionEvent_RoundTrip(t *testing.T) {
	rec, ok := decodeSessionEvent(`{"session_id":"s1","harness":"claude","account":"a1","state":"active"}`)
	if !ok {
		t.Fatal("decodeSessionEvent: expected ok=true for a valid record")
	}
	if rec.SessionID != "s1" || rec.Harness != "claude" || rec.Account != "a1" || rec.State != "active" {
		t.Errorf("decodeSessionEvent round-trip mismatch: %+v", rec)
	}
}

// TestDecodeSessionEvent_MalformedNeverPanics proves malformed input is
// skipped (ok=false), never a panic - the untrusted-input contract every
// SSE data block carries.
func TestDecodeSessionEvent_MalformedNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("decodeSessionEvent panicked on malformed input: %v", r)
		}
	}()
	for _, in := range []string{"", "not json", "{", `{"session_id":123}`} {
		if _, ok := decodeSessionEvent(in); ok {
			t.Errorf("decodeSessionEvent(%q) = ok true, want false", in)
		}
	}
}

// TestEmbeddedFleetSessionRows_RunsWithoutDaemon proves the D/S-07.T4
// embedded path (a live census scan, no persisted domain store) runs to
// completion with no daemon and no panic, on every platform this ticket
// targets - it never dials a socket, so it belongs in this file's
// no-network unit lane.
func TestEmbeddedFleetSessionRows_RunsWithoutDaemon(t *testing.T) {
	rows, err := embeddedFleetSessionRows()
	if err != nil {
		t.Fatalf("embeddedFleetSessionRows: unexpected error: %v", err)
	}
	for _, r := range rows {
		if r.SessionID == "" || r.Elapsed != "-" {
			t.Errorf("embedded row shape unexpected: %+v", r)
		}
	}
}

// TestFleetSessionsWatch_WindowsTier2Refusal proves --watch refuses on
// Windows before ever attempting to dial. This ticket's files_scope
// names exactly fleet_test.go (no sibling fleet_windows_test.go), so
// this test self-skips off-Windows and runs for real on Windows CI,
// mirroring internal/fleet/sessions/rpc_test.go's own precedent for the
// identical GOOS-gated shape.
func TestFleetSessionsWatch_WindowsTier2Refusal(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("this refusal is GOOS-gated (fleet_watch.go); only Windows CI actually exercises it")
	}
	cmd := newFleetSessionsCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"--watch"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "Windows tier-2") {
		t.Fatalf("fleet sessions --watch on windows = %v, want a Windows tier-2 refusal", err)
	}
}

// TestFleetSessionsErrors_AreActionable proves both --watch refusals
// carry a concrete next step, never a bare "failed" message.
func TestFleetSessionsErrors_AreActionable(t *testing.T) {
	if !strings.Contains(errWatchNoDaemon.Error(), "cascade daemon run") {
		t.Errorf("errWatchNoDaemon does not suggest starting the daemon: %v", errWatchNoDaemon)
	}
	if !strings.Contains(errWatchWindowsTier2.Error(), "--json") {
		t.Errorf("errWatchWindowsTier2 does not suggest the --json equivalent: %v", errWatchWindowsTier2)
	}
}
