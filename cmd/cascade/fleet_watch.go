// Purpose: `cascade fleet sessions --watch`'s SSE half, split out of
//
//	fleet.go solely to keep that file under the repo-wide 300-line cap
//	(a hard, CI-enforced gate) once the daemon-required refusal logic
//	and the SSE read loop were added — see fleet.go's header for the
//	CONTRACT DEVIATION note on why this dials the socket directly
//	rather than through internal/client's unexported streamClient.
//
// CONTRACT DEVIATION (files_scope, recorded, not papered over). T-2's
// files_scope.add lists exactly cmd/cascade/fleet.go,
// cmd/cascade/fleet_test.go and the testscript. This file is a fourth,
// added because the 300-line-per-file gate (a repo-wide hard rule) left
// no room for --watch's refusal + SSE-read-loop logic inside fleet.go
// alongside the one-shot path. Both files are this ticket's own new
// surface, not another agent's package.
//
// SPORT: cmd/cascade/fleet (ADD, per T-2 sport_updates; watch half).
package main

import (
	"context"
	"io"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errWatchWindowsTier2 is --watch's Windows refusal: Windows tier-2 has
// no daemon at all (06 §2), so there is nothing to subscribe to.
var errWatchWindowsTier2 = cascade.New(cascade.KindUnsupported, "cascade fleet sessions --watch: unavailable on Windows tier-2 (no daemon); use `cascade fleet sessions --json` instead (automation parity, 06 §5.8)")

// errWatchNoDaemon is --watch's refusal when the daemonless probe found
// no reachable socket: an actionable message, never a panic.
var errWatchNoDaemon = cascade.New(cascade.KindUnavailable, "cascade fleet sessions --watch: no daemon socket reachable; start it with `cascade daemon run`, or use `cascade fleet sessions --json` for the one-shot equivalent")

// runFleetSessionsWatch refuses on Windows or when no daemon is
// reachable, otherwise streams fleet.sessions.changed events until the
// command's context is canceled.
func runFleetSessionsWatch(cmd *cobra.Command, deps fleetSessionsDeps) error {
	if goruntime.GOOS == "windows" {
		return errWatchWindowsTier2
	}
	ctx := cmd.Context()
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return errWatchNoDaemon
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return err
	}
	body, closeFn, err := dialFleetSessionsEvents(ctx, deps, settings.SocketPath)
	if err != nil {
		return err
	}
	defer closeFn()
	w := fleetSessionsOutputWriter(cmd)
	return watchFleetSessionsLoop(ctx, w, body)
}

// dialFleetSessionsEvents opens the daemon's /events stream filtered to
// the fleet session topic. The transport is daemon_events_dial.go's single
// client - the same one the harness watch uses - never a hand-rolled
// second one, and the topic comes from the package that publishes it so
// the two cannot drift.
func dialFleetSessionsEvents(ctx context.Context, deps fleetSessionsDeps, socketPath string) (io.ReadCloser, func(), error) {
	return dialDaemonEvents(ctx, deps.DialContext, socketPath, sessions.Topic)
}

// watchFleetSessionsLoop reads body's SSE stream line by line, folding
// "data:" lines (per the WHATWG SSE grammar) into complete session-
// changed events, and renders each: a full table refresh on TTY, one
// NDJSON line on non-TTY (D/S-06.T5's stream contract). Never panics on
// malformed input - an undecodable data block is skipped, not fatal.
func watchFleetSessionsLoop(ctx context.Context, w *output.Writer, body io.Reader) error {
	records := make(chan sessions.SessionRecord, 16)
	go func() {
		defer close(records)
		sessions.ReadRecords(ctx, body, records)
	}()

	seen := map[string]sessions.SessionRecord{}
	for rec := range records {
		seen[rec.SessionID] = rec
		if err := renderFleetSessionsWatchUpdate(w, seen, rec); err != nil {
			return err
		}
	}
	return nil
}

// renderFleetSessionsWatchUpdate renders the just-arrived event: one
// NDJSON line for it on non-TTY, a full refresh of every known session
// (seen) on TTY.
func renderFleetSessionsWatchUpdate(w *output.Writer, seen map[string]sessions.SessionRecord, latest sessions.SessionRecord) error {
	if !w.IsTTY() {
		return w.NDJSON().Emit(latest)
	}
	rows := make([]fleetSessionRow, 0, len(seen))
	for _, rec := range seen {
		rows = append(rows, fleetSessionRow{
			SessionID: rec.SessionID, Binary: rec.Harness, State: rec.State,
			Confidence: "n/a", Account: rec.Account, Elapsed: formatElapsed(rec.UpdatedAt, runtime.NewSystemClock()),
		})
	}
	w.Println(fleetSessionRows(rows).String())
	return nil
}
