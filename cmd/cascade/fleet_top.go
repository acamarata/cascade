// Purpose: `cascade fleet top` (P1-E18-W4-S40-T1, 07-CLI-COMMAND-TREE
//
//	§fleet top entry) plus the hidden top-level alias `cascade top`
//	(R-14.92). Wires internal/fleet's Fetcher/Model/DecodeSessionEvent
//	(fully unit-tested there without a TTY) to the real daemon:
//	fleet.sessions.list over internal/client (D/S-07.T3), the GET
//	/events SSE stream (D/S-06.T4, reusing dialFleetSessionsEvents's
//	exact dial pattern from fleet_watch.go), and a locally-constructed
//	governor.Sampler for the resource panel (see internal/fleet/
//	top_fetch.go's SamplerGovernorReader doc comment for the disclosed
//	no-daemon-side-governor-RPC gap this covers).
//
// Inputs: cobra args/flags; fleetSessionsDeps, the same injected deps
//
//	every other fleet subcommand threads through.
//
// Outputs: an interactive bubbletea Program (TTY), or a single JSON
//
//	snapshot to stdout (--once --json, 06 §5.8 automation parity).
//
// Constraints: Windows tier-2 has no daemon at all — this command
//
//	refuses immediately there, before touching the client or the
//	sampler, matching D/S-06.T2's own tier-2 refusal pattern.
//
// SPORT: cmd/cascade/fleet-top-cli (ADD, P1-E18-W4-S40-T1).
package main

import (
	"context"
	"io"
	goruntime "runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet"
	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// errFleetTopWindowsTier2 is `cascade fleet top`'s unconditional Windows
// refusal: Windows tier-2 has no daemon at all (06 §2), so there is
// nothing for the sessions table, the governor panel's daemon-side data,
// or the SSE stream to connect to.
var errFleetTopWindowsTier2 = cascade.New(cascade.KindUnsupported,
	"cascade fleet top: daemon not available on Windows tier-2")

// errFleetTopNoDaemon mirrors errFleetJobsNoDaemon's exact refusal shape.
var errFleetTopNoDaemon = cascade.New(cascade.KindUnavailable,
	"cascade fleet top: no daemon socket reachable; start it with `cascade daemon run`")

// mountFleetTopAlias registers the hidden top-level `cascade top` alias
// for `cascade fleet top`, mirroring mountFleetSessionsAlias's exact
// pattern so the two commands can never drift apart.
func mountFleetTopAlias(root *cobra.Command, deps fleetSessionsDeps) {
	cmd := newFleetTopCmd(deps)
	cmd.Use = "top"
	cmd.Hidden = true
	root.AddCommand(cmd)
}

// newFleetTopCmd builds `fleet top [--once]`. --json is the root's
// persistent global flag, matching every other fleet subcommand.
func newFleetTopCmd(deps fleetSessionsDeps) *cobra.Command {
	var once bool
	cmd := &cobra.Command{
		Use:   "top",
		Short: "Live fleet dashboard: sessions, governor, active task, summary",
		Long: "Renders a live four-panel terminal dashboard (sessions, governor resources,\n" +
			"active task, fleet summary) fed by the daemon's SSE stream. `--once --json`\n" +
			"emits a single machine-readable snapshot and exits (automation parity, 06 §5.8).\n" +
			"Unavailable on Windows tier-2 (no daemon).",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if goruntime.GOOS == "windows" {
				return errFleetTopWindowsTier2
			}
			if once {
				return runFleetTopOnce(cmd, deps)
			}
			return runFleetTopInteractive(cmd, deps)
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "emit one snapshot and exit (use with --json)")
	return cmd
}

// fleetTopClient dials the daemon and reports the daemonless refusal,
// mirroring dialFleetJobsClient's exact pattern.
func fleetTopClient(ctx context.Context, deps fleetSessionsDeps) (*client.Client, error) {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return nil, errFleetTopNoDaemon
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), fleetSessionsDialTimeout), nil
}

// sessionListerAdapter adapts sessions.Client into fleet.SessionLister.
type sessionListerAdapter struct {
	inner *sessions.Client
	clk   runtime.Clock
}

func (a sessionListerAdapter) List(ctx context.Context) ([]fleet.TopSessionRow, error) {
	recs, err := a.inner.List(ctx, sessions.Filter{})
	if err != nil {
		return nil, err
	}
	rows := make([]fleet.TopSessionRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, fleet.TopSessionRow{
			SessionID: r.SessionID, Harness: r.Harness, State: r.State,
			Account: r.Account, Elapsed: formatElapsed(r.UpdatedAt, a.clk),
		})
	}
	return rows, nil
}

// newFleetTopFetcher builds the shared Fetcher both --once and the
// interactive path use, wiring a real local governor.Sampler (started
// immediately, so it has the most possible time to land its first tick
// before this command's first read — see top_fetch.go's
// SamplerGovernorReader doc comment for the disclosed gap this covers:
// a fast `--once` invocation commonly still reports Available=false,
// which is an accurate empty state, not a bug).
func newFleetTopFetcher(c *client.Client, clk runtime.Clock) (*fleet.Fetcher, func()) {
	sampler := governor.NewSampler(governor.SamplerConfig{MaxHz: governor.MaxSamplerHz}, clk, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	sampler.Start(ctx)
	stop := func() { sampler.Stop(); cancel() }
	f := &fleet.Fetcher{
		Sessions: sessionListerAdapter{inner: sessions.NewClient(c), clk: clk},
		Governor: fleet.SamplerGovernorReader{Sampler: sampler},
		Task:     fleet.UnavailableTaskReader{},
		Clock:    clk,
	}
	return f, stop
}

// topSnapshotResult adapts a fleet.TopSnapshot to output.Writer.Result's
// dual JSON/human contract: MarshalJSON drives --json mode (wrapped in
// the standard OK envelope, matching every other fleet subcommand);
// String() drives the human-mode fallback.
type topSnapshotResult struct{ snap fleet.TopSnapshot }

func (r topSnapshotResult) MarshalJSON() ([]byte, error) { return fleet.RenderSnapshotJSON(r.snap) }
func (r topSnapshotResult) String() string               { return fleet.RenderFrame(r.snap, 100, 30) }

// runFleetTopOnce implements `--once [--json]`: a real, complete
// snapshot, never a placeholder subset.
func runFleetTopOnce(cmd *cobra.Command, deps fleetSessionsDeps) error {
	ctx := cmd.Context()
	c, err := fleetTopClient(ctx, deps)
	if err != nil {
		return err
	}
	clk := runtime.NewSystemClock()
	f, stop := newFleetTopFetcher(c, clk)
	defer stop()
	snap, err := f.Fetch(ctx)
	if err != nil {
		return err
	}
	return fleetSessionsOutputWriter(cmd).Result(topSnapshotResult{snap})
}

// runFleetTopInteractive refuses when no daemon is reachable, otherwise
// runs the live bubbletea Program: an initial fetch, then a background
// goroutine feeding fleet.SessionEventMsg/fleet.DisconnectMsg from the
// daemon's SSE stream via p.Send — the same message shapes
// internal/fleet/top_test.go drives Model.Update with directly.
func runFleetTopInteractive(cmd *cobra.Command, deps fleetSessionsDeps) error {
	ctx := cmd.Context()
	c, err := fleetTopClient(ctx, deps)
	if err != nil {
		return err
	}
	clk := runtime.NewSystemClock()
	f, stop := newFleetTopFetcher(c, clk)
	defer stop()
	initial, err := f.Fetch(ctx)
	if err != nil {
		return err
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

	p := tea.NewProgram(fleet.NewModel(initial, clk), tea.WithContext(ctx), tea.WithOutput(cmd.OutOrStdout()))
	go pumpFleetTopSSE(p, body, clk)
	_, err = p.Run()
	return err
}

// pumpFleetTopSSE drives fleet.FoldSSE over body's real SSE stream,
// forwarding each decoded row into p as a fleet.SessionEventMsg via the
// onRow callback (FoldSSE's real production caller — top_test.go's own
// replay test is the other, passing nil). FoldSSE blocks until the
// stream ends (io.EOF) or errors; either way this sends one terminal
// fleet.DisconnectMsg — never a silent retry loop.
func pumpFleetTopSSE(p *tea.Program, body io.Reader, clk runtime.Clock) {
	fleet.FoldSSE(body, fleet.TopSnapshot{}, clk, func(row fleet.TopSessionRow) {
		p.Send(fleet.SessionEventMsg{Row: row})
	})
	p.Send(fleet.DisconnectMsg{Err: cascade.New(cascade.KindUnavailable, "cascade fleet top: SSE stream ended")})
}
