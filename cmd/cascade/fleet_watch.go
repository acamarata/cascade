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
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	goruntime "runtime"
	"strings"

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

// dialFleetSessionsEvents opens GET /events?topic=fleet.sessions against
// the daemon socket, reusing client.UnixDialer-shaped dial functions -
// the same exported dial seam every command uses, never a hand-rolled
// second transport.
func dialFleetSessionsEvents(ctx context.Context, deps fleetSessionsDeps, socketPath string) (io.ReadCloser, func(), error) {
	httpClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return deps.DialContext(ctx, socketPath)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/events?topic=fleet.sessions", nil)
	if err != nil {
		return nil, func() {}, cascade.Wrap(cascade.KindInternal, err, "cascade fleet sessions --watch: build request")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, func() {}, cascade.Wrap(cascade.KindUnavailable, err, "cascade fleet sessions --watch: dial /events")
	}
	return resp.Body, func() { _ = resp.Body.Close() }, nil
}

// watchFleetSessionsLoop reads body's SSE stream line by line, folding
// "data:" lines (per the WHATWG SSE grammar) into complete session-
// changed events, and renders each: a full table refresh on TTY, one
// NDJSON line on non-TTY (D/S-06.T5's stream contract). Never panics on
// malformed input - an undecodable data block is skipped, not fatal.
func watchFleetSessionsLoop(ctx context.Context, w *output.Writer, body io.Reader) error {
	seen := map[string]sessions.SessionRecord{}
	scanner := bufio.NewScanner(body)
	var data []string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			continue
		}
		if line != "" || len(data) == 0 {
			continue
		}
		rec, ok := decodeSessionEvent(strings.Join(data, "\n"))
		data = nil
		if !ok {
			continue
		}
		seen[rec.SessionID] = rec
		if err := renderFleetSessionsWatchUpdate(w, seen, rec); err != nil {
			return err
		}
	}
	return nil
}

// decodeSessionEvent decodes one SSE "data:" block as a
// fleet.sessions.changed payload (domain.go's Store.emit marshals a
// bare SessionRecord).
func decodeSessionEvent(data string) (sessions.SessionRecord, bool) {
	var rec sessions.SessionRecord
	if err := json.Unmarshal([]byte(data), &rec); err != nil {
		return sessions.SessionRecord{}, false
	}
	return rec, true
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
