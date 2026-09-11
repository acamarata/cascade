// Purpose: `cascade fleet bench <lane-id> [--n N] [--concurrency C]`
//
//	(P1-E12-W3-S25-T5, 07-CLI-COMMAND-TREE §fleet note 8): a one-shot,
//	headless CLI door onto the daemon's fleet.bench_lane JSON-RPC 2.0
//	method (internal/fleet/rpc.go). Split out of fleet.go solely to keep
//	that file under the repo-wide 300-line cap, mirroring
//	fleet_watch.go's own documented precedent for a fourth file (see
//	this file's CONTRACT DEVIATION note).
//
// CONTRACT DEVIATION (files_scope, recorded, not papered over). This
// ticket's files_scope.change lists exactly cmd/cascade/fleet.go for the
// CLI half. fleet_watch.go already established the precedent (P1-
// E12-W3-S24-T3) that a fourth cmd/cascade file is this ticket's own new
// surface, not another agent's package, when the 300-line-per-file gate
// leaves no room for a new subcommand's refusal + dispatch logic inside
// fleet.go alongside sessions'. This file is that same shape for bench.
//
// CONTRACT DEVIATION (no local fallback, recorded, not papered over). "a
// local-capable CLI path ... may run headless" (the ticket's HOW step)
// reads as: bench does not need the daemon's SSE stream, only a plain
// one-shot RPC round trip, matching CLI-COMMAND-TREE note 8's actual
// meaning ("local-capable" = no interactive/streaming session required).
// It does NOT mean bench can run without a daemon at all: a probe needs
// a live pkg/provider.ModelExecutor (Conductor), and there is no local,
// daemonless equivalent in this tree yet (the same composition-root gap
// internal/fleet/rpc.go's own CONTRACT DEVIATION note and this ticket's
// testonly-allow.json entries document). No daemon reachable is
// therefore a structured refusal, not a degraded local execution.
//
// SPORT: cmd/cascade/fleet (ADD, per T-5 sport_updates; bench half).
package main

import (
	"context"
	goruntime "runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fleetBenchDialTimeout bounds the one-shot round trip against the
// daemon, matching fleetSessionsDialTimeout's convention but longer:
// bench itself may run several probes server-side before responding
// (BenchConfig's own N/Concurrency), not just one status lookup.
const fleetBenchDialTimeout = 30 * time.Second

// errBenchWindowsTier2 mirrors errWatchWindowsTier2: Windows tier-2 has
// no daemon at all (06 §2), so there is nothing to dial.
var errBenchWindowsTier2 = cascade.New(cascade.KindUnsupported,
	"cascade fleet bench: unavailable on Windows tier-2 (no daemon); bench requires a live daemon-managed provider lane")

// errBenchNoDaemon mirrors errWatchNoDaemon: an actionable refusal, never
// a panic, when the daemonless probe found no reachable socket.
var errBenchNoDaemon = cascade.New(cascade.KindUnavailable,
	"cascade fleet bench: no daemon socket reachable; start it with `cascade daemon run`")

// benchLaneRequest is fleet.bench_lane's wire request shape, mirrored
// here rather than importing internal/fleet's unexported benchLaneParams
// (this file only needs the JSON shape, not the decoding logic).
type benchLaneRequest struct {
	LaneID string            `json:"lane_id"`
	Config fleet.BenchConfig `json:"config,omitempty"`
}

// newFleetBenchCmd builds `fleet bench <lane-id> [--n N] [--concurrency C]`.
func newFleetBenchCmd(deps fleetSessionsDeps) *cobra.Command {
	var n, concurrency int
	cmd := &cobra.Command{
		Use:   "bench <lane-id>",
		Short: "Probe/bench a provider lane's health (one-shot, headless; requires a running daemon)",
		Long: "Probe/bench a provider lane through fleet.bench_lane: N probes at the given\n" +
			"concurrency, reporting p50/p95 latency, error rate, and a cost estimate.\n" +
			"Headless and one-shot (07 §fleet note 8) - no SSE stream is opened - but a\n" +
			"live daemon is still required: a probe dispatches through a real provider\n" +
			"lane, which only the daemon's composition root can construct.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := runFleetBenchLane(cmd.Context(), deps, args[0], n, concurrency)
			if err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(result)
		},
	}
	cmd.Flags().IntVar(&n, "n", 1, "probe count")
	cmd.Flags().IntVar(&concurrency, "concurrency", 1, "probe concurrency")
	return cmd
}

// runFleetBenchLane refuses on Windows or when no daemon is reachable,
// otherwise dials fleet.bench_lane and returns its BenchResult.
func runFleetBenchLane(ctx context.Context, deps fleetSessionsDeps, laneID string, n, concurrency int) (fleet.BenchResult, error) {
	if goruntime.GOOS == "windows" {
		return fleet.BenchResult{}, errBenchWindowsTier2
	}
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return fleet.BenchResult{}, errBenchNoDaemon
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return fleet.BenchResult{}, err
	}
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), fleetBenchDialTimeout)
	req := benchLaneRequest{LaneID: laneID, Config: fleet.BenchConfig{N: n, Concurrency: concurrency}}
	var result fleet.BenchResult
	if err := c.Do(ctx, fleet.MethodBenchLane, req, &result); err != nil {
		return fleet.BenchResult{}, err
	}
	return result, nil
}
