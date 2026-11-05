// Purpose: `cascade fleet learn status|explain|reset` (P1-LRN-04). The
//
//	group inspects and resets the capability scorer's learned observations
//	through the daemon's learn.status, learn.explain and learn.reset verbs.
//
// Inputs: cobra args/flags; a fleetSessionsDeps injected at construction so
//
//	no test touches the real environment or a real socket.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure.
//
// Constraints: every verb is AccessOwnerOnly (a principal never reaches
// learn state). The commands never prompt and never read stdin, so
// CASCADE_NO_INPUT=1 with a closed stdin changes nothing. cmd/cascade does
// not import internal/rpc to dial the daemon; it declares its own wire
// shapes, matching fleet_mode.go's precedent.
//
// SPORT: cmd/cascade/fleet-learn-cli (ADD, P1-LRN-04).
package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// learnStatusMethod, learnExplainMethod and learnResetMethod are the
// daemon verbs this group dials.
const (
	learnStatusMethod  = "learn.status"
	learnExplainMethod = "learn.explain"
	learnResetMethod   = "learn.reset"
)

// The learn verbs are owner-only (a caller principal never reads or resets
// the scorer's observations). They are declared once, beside the daemon
// handler, in internal/learn/rpc_learn_access.go; a second declaration here
// would panic at init.

// errFleetLearnNoDaemon is the actionable refusal when no daemon socket
// is reachable.
var errFleetLearnNoDaemon = cascade.New(cascade.KindUnavailable,
	"cascade fleet learn: no daemon socket reachable; start it with `cascade daemon run`")

// dialFleetLearnClient builds a client dialing the daemon. A daemonless or
// embedded state refuses without dialing.
func dialFleetLearnClient(ctx context.Context, deps fleetSessionsDeps) (*client.Client, error) {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return nil, errFleetLearnNoDaemon
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), fleetJobsDialTimeout), nil
}

// learnScoreRow mirrors one posterior row of the learn.status reply.
type learnScoreRow struct {
	Tier          string  `json:"tier"`
	TaskClass     string  `json:"task_class"`
	Score         float64 `json:"score"`
	Alpha         float64 `json:"alpha"`
	Beta          float64 `json:"beta"`
	Observations  int     `json:"observations"`
	FallbackLevel string  `json:"fallback_level"`
}

// newFleetLearnCmd builds the `fleet learn` command group.
func newFleetLearnCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "learn",
		Short: "Inspect, explain and reset the capability scorer's learned observations",
	}
	cmd.AddCommand(newFleetLearnStatusCmd(deps))
	cmd.AddCommand(newFleetLearnExplainCmd(deps))
	cmd.AddCommand(newFleetLearnResetCmd(deps))
	return cmd
}
