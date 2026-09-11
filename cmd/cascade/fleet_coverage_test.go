//go:build !windows

// Purpose: additional unit-lane coverage for `cascade fleet sessions`'s
//
//	dial-adjacent code paths that fleet_test.go's original suite left at
//	0% - runFleetSessionsOnce/fleetSessionsOutputWriter's success path,
//	fetchFleetSessions's routing decision, fetchFleetSessionsDaemon's
//	real dial-failure branch, runFleetSessionsWatch's no-daemon and
//	dial-failure branches, and watchFleetSessionsLoop/
//	renderFleetSessionsWatchUpdate's SSE folding logic. This file
//	deliberately imports neither "net" nor "net/http": every dial it
//	exercises borrows the REAL production DialContext closure as an
//	already-typed function value (productionFleetSessionsDeps().
//	DialContext) against a socket path nothing listens on, the same
//	technique status_test.go's TestFetchStatus_DaemonNotRunning
//	establishes - a local-only ENOENT syscall, not network I/O
//	(internal/build's no-network-unit-lane gate, Art.7.2).
//
// SPORT: cmd/cascade/fleet (CHANGE, coverage-only).
package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
)

// newHermeticFleetDeps mirrors status_test.go's newHermeticStatusDeps: a
// fresh temp-dir path provider with the REAL production DialContext
// closure, so fetchFleetSessionsDaemon/dialFleetSessionsEvents run their
// real bodies against a socket path with no listener.
func newHermeticFleetDeps(t *testing.T) fleetSessionsDeps {
	t.Helper()
	dir := t.TempDir()
	deps := productionFleetSessionsDeps()
	deps.Paths = fakeDaemonPaths{root: dir}
	return deps
}

// newTestFleetCmd builds a fleet-sessions cobra command with the four
// global output flags registered locally, mirroring
// TestStatusCommand_NoArgsRejected's pattern, so fleetSessionsOutputWriter
// can read them without a root mount.
func newTestFleetCmd(deps fleetSessionsDeps) (*cobra.Command, *bytes.Buffer) {
	cmd := newFleetSessionsCmd(deps)
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("quiet", false, "")
	cmd.Flags().Bool("verbose", false, "")
	cmd.Flags().Bool("no-color", false, "")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

// TestRunFleetSessionsOnce_EmbeddedSuccess drives the real cobra RunE with
// no daemonless state on the context (DaemonlessStateFrom's ok=false
// case), so fetchFleetSessions routes to the embedded path and
// runFleetSessionsOnce's success branch renders a table through
// fleetSessionsOutputWriter - covering both previously-0% functions.
func TestRunFleetSessionsOnce_EmbeddedSuccess(t *testing.T) {
	cmd, buf := newTestFleetCmd(fleetSessionsDeps{})
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("fleet sessions: unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "SESSION_ID") {
		t.Errorf("output missing table header: %q", buf.String())
	}
}

// TestFetchFleetSessions_NoStateRoutesEmbedded proves the ok=false branch
// (root probe never ran, e.g. a bare context) is treated as embedded, not
// daemon, and returns successfully without dialing anything.
func TestFetchFleetSessions_NoStateRoutesEmbedded(t *testing.T) {
	rows, err := fetchFleetSessions(context.Background(), fleetSessionsDeps{})
	if err != nil {
		t.Fatalf("fetchFleetSessions: unexpected error routing embedded: %v", err)
	}
	for _, r := range rows {
		if r.Elapsed != "-" {
			t.Errorf("expected the embedded row shape (Elapsed=\"-\"), got %+v", r)
		}
	}
}

// TestFetchFleetSessions_LiveStateRoutesDaemon attaches a DaemonlessState
// with Embedded=false (a confirmed-live daemon) and hermetic deps whose
// socket has no listener, proving the function actually dispatched to
// fetchFleetSessionsDaemon (which fails to dial) rather than silently
// falling back to the embedded path (which would succeed with no error).
func TestFetchFleetSessions_LiveStateRoutesDaemon(t *testing.T) {
	deps := newHermeticFleetDeps(t)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	_, err := fetchFleetSessions(ctx, deps)
	if err == nil {
		t.Fatal("fetchFleetSessions: expected a dial error when routed to the daemon path")
	}
}

// TestFetchFleetSessionsDaemon_DaemonNotRunning exercises
// fetchFleetSessionsDaemon's real body (settings resolution, client
// construction, and the sessions.List round trip) against a socket
// nothing listens on.
func TestFetchFleetSessionsDaemon_DaemonNotRunning(t *testing.T) {
	deps := newHermeticFleetDeps(t)
	_, err := fetchFleetSessionsDaemon(context.Background(), deps)
	if err == nil {
		t.Fatal("fetchFleetSessionsDaemon: expected an error against a socket nothing listens on")
	}
}

// TestRunFleetSessionsWatch_NoProbeState proves --watch refuses with
// errWatchNoDaemon when the root's daemonless probe never ran (ok=false),
// never attempting to dial.
func TestRunFleetSessionsWatch_NoProbeState(t *testing.T) {
	cmd, _ := newTestFleetCmd(fleetSessionsDeps{})
	cmd.SetContext(context.Background())
	err := runFleetSessionsWatch(cmd, fleetSessionsDeps{})
	if err == nil || !strings.Contains(err.Error(), "no daemon socket reachable") {
		t.Fatalf("runFleetSessionsWatch = %v, want errWatchNoDaemon", err)
	}
}

// TestRunFleetSessionsWatch_EmbeddedState proves --watch refuses the same
// way when the probe confirmed embedded mode (Embedded=true).
func TestRunFleetSessionsWatch_EmbeddedState(t *testing.T) {
	cmd, _ := newTestFleetCmd(fleetSessionsDeps{})
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: true})
	cmd.SetContext(ctx)
	err := runFleetSessionsWatch(cmd, fleetSessionsDeps{})
	if err == nil || !strings.Contains(err.Error(), "no daemon socket reachable") {
		t.Fatalf("runFleetSessionsWatch = %v, want errWatchNoDaemon", err)
	}
}

// TestRunFleetSessionsWatch_DialFails proves that once the probe confirms
// a live daemon, --watch proceeds past both refusal checks into
// dialFleetSessionsEvents, and surfaces its real dial failure against a
// socket nothing listens on - never a panic, never a silent embedded
// fallback.
func TestRunFleetSessionsWatch_DialFails(t *testing.T) {
	deps := newHermeticFleetDeps(t)
	cmd, _ := newTestFleetCmd(deps)
	ctx := runtime.WithDaemonlessState(context.Background(), runtime.DaemonlessState{Embedded: false})
	cmd.SetContext(ctx)
	err := runFleetSessionsWatch(cmd, deps)
	if err == nil {
		t.Fatal("runFleetSessionsWatch: expected a dial error against a socket nothing listens on")
	}
	if !strings.Contains(err.Error(), "dial /events") {
		t.Errorf("err = %v, want it to mention dialing /events", err)
	}
}

// TestWatchFleetSessionsLoop_ContextCanceledStopsEarly proves an
// already-canceled context stops the read loop on its first check,
// rendering nothing.
func TestWatchFleetSessionsLoop_ContextCanceledStopsEarly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := strings.NewReader("data: {\"session_id\":\"s1\"}\n\n")
	buf := &bytes.Buffer{}
	w := output.New(buf, &bytes.Buffer{}, false, false, false, true)
	if err := watchFleetSessionsLoop(ctx, w, body); err != nil {
		t.Fatalf("watchFleetSessionsLoop: unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected nothing rendered after cancellation, got %q", buf.String())
	}
}

// TestWatchFleetSessionsLoop_FoldsAndSkips drives the SSE fold loop
// through every branch its body distinguishes: a stray non-data line, a
// blank line with nothing accumulated, a malformed data block (skipped,
// never fatal), and a well-formed one that reaches
// renderFleetSessionsWatchUpdate's non-TTY (NDJSON) branch.
func TestWatchFleetSessionsLoop_FoldsAndSkips(t *testing.T) {
	body := strings.NewReader(
		"\n" + // blank line, nothing accumulated: skipped
			"id: 1\n" + // stray non-data field: skipped
			"data: not json\n" + // malformed: decodeSessionEvent returns ok=false
			"\n" + // terminates the malformed block
			`data: {"session_id":"s1","harness":"claude","account":"a1","state":"active"}` + "\n" +
			"\n", // terminates the valid block: renders
	)
	buf := &bytes.Buffer{}
	w := output.New(buf, &bytes.Buffer{}, false, false, false, true)
	if err := watchFleetSessionsLoop(context.Background(), w, body); err != nil {
		t.Fatalf("watchFleetSessionsLoop: unexpected error: %v", err)
	}
	if !strings.Contains(buf.String(), "s1") {
		t.Errorf("expected the valid event's session_id in output, got %q", buf.String())
	}
}

// TestProductionFleetSessionsDeps_DialContextFailsForMissingSocket proves
// the real production DialContext closure this file borrows throughout
// actually fails fast (a local ENOENT) against a path nothing listens on,
// same as status_test.go's equivalent proof for statusDeps.
func TestProductionFleetSessionsDeps_DialContextFailsForMissingSocket(t *testing.T) {
	dial := productionFleetSessionsDeps().DialContext
	missing := filepath.Join(t.TempDir(), "nothing-listens-here.sock")
	if _, err := dial(context.Background(), missing); err == nil {
		t.Fatal("DialContext: expected an error dialing a socket nothing listens on")
	}
}
