// Purpose: `cascade fleet capacity [--json]` (07-CLI-COMMAND-TREE §fleet,
// amended R-16.21: "fleet absorbs capacity"). Dials the fleet.capacity
// JSON-RPC 2.0 method (internal/fleet/capacity/rpc.go, S-63.T1) through
// the Go client SDK, mirroring fleet_jobs.go/fleet_leases.go's exact
// dialFleetJobsClient pattern — there is no embedded fallback: a
// FleetSnapshot is the DAEMON's own in-memory Compositor state (S-63.T1),
// not something a standalone process can reconstruct, so an unreachable
// daemon is a plain refusal, never a silently degraded local read.
//
// Inputs: cobra args/flags; the SAME fleetSessionsDeps injection every
// other fleet subcommand in this package already uses.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
// error on failure. Never a bare fmt.Print.
//
// Constraints: read-only verb. --json is the root's existing persistent
// global flag (07 §global-flags), not a local one — see the CONTRACT
// DEVIATION note below.
//
// CONTRACT DEVIATION (recorded, not papered over — three points).
//
// (1) --json flag. This ticket's task 3 says to "add the `--json`
// persistent flag," but root.go (cmd/cascade/root.go:100) already
// registers --json as a persistent flag on the root command, exactly as
// `fleet sessions`/`fleet leases`/`fleet jobs` already consume it via
// fleetSessionsOutputWriter. A second, command-local --json flag would
// shadow the global one rather than complementing it. This file adds no
// flag of its own and reads the inherited global exactly like its
// siblings.
//
// (2) JSON envelope, not a bare struct. The ticket's full_desc says
// "JSON mode (--json): marshals the full FleetSnapshot struct as
// canonical JSON," and its acceptance criteria describe an empty
// snapshot as emitting literally `{"providers":[],"nodes":[]}`. Every
// other --json command in this tree (fleet sessions/leases/jobs, status,
// context show, ...) instead routes through internal/output.Writer.Result,
// which wraps the payload in the versioned Envelope (internal/output/
// envelope.go: `{"version":1,"ok":true,"data":{...}}`) — a repo-wide,
// documented wire contract (docs/cli-output-contract.md) that lets every
// script parsing --json output check one shape regardless of command.
// Forking a bespoke unwrapped-JSON renderer here would break that
// contract for this one command. This file uses Result like every
// sibling command; the FleetSnapshot appears verbatim (full struct,
// canonical json.Marshal field order) under the envelope's "data" key.
//
// (3) providers is an object, not an array. FleetSnapshot.Providers
// (internal/fleet/capacity/snapshot.go) is `map[string]ProviderSlot`,
// keyed by profile ref — a real, load-bearing shape from S-63.T1, out of
// this ticket's files_scope to change. Its JSON encoding is therefore an
// object (`{}` when empty), never an array (`[]`). fleet_capacity_test.go
// asserts the real shape (a "providers" object key present under
// envelope.data, decodable via encoding/json into map[string]ProviderSlot)
// rather than the ticket's literal "top-level providers array" text.
//
// (4) tier column omitted from the human table. The ticket's full_desc
// names a "tier" column ("provider | tier | state | ..."), but
// ProviderSlot (snapshot.go) carries no tier field — registry.Tier
// (the lane-affinity vocabulary) lives on registry.ProviderRecord and is
// not propagated into ProviderSlot by compositor.go's buildProviderSlot,
// both files outside this ticket's files_scope. Fabricating a "-" or
// zero-value tier column would be exactly the kind of stub Art.1
// forbids. The table below renders the six columns FleetSnapshot's real
// data supports: provider, state, the three bucket usage percentages,
// and reset_in.
//
// SPORT: cmd/cascade/fleet-capacity-cli (ADD, P1-E31-W6-S63-T4).
package main

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/fleet/capacity"
)

// newFleetCapacityCmd builds `fleet capacity`.
func newFleetCapacityCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "capacity",
		Short: "Show the daemon's composed fleet capacity snapshot",
		Long: "Show the fleet.capacity snapshot: one row per provider profile with its\n" +
			"state and the three capacity buckets (interactive_usage, agent_sdk_credit,\n" +
			"api_credit). Requires a running daemon (`cascade daemon run`) — a\n" +
			"FleetSnapshot is the daemon's own in-memory state, not something this\n" +
			"command can reconstruct standalone. --json emits the same versioned\n" +
			"envelope every other cascade command does, with the full FleetSnapshot\n" +
			"under \"data\". Output is identical with CASCADE_NO_INPUT=1: this command\n" +
			"never prompts.",
		Example: "  cascade fleet capacity\n" +
			"  cascade fleet capacity --json",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			snap, err := fetchFleetCapacitySnapshot(cmd.Context(), deps)
			if err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(fleetCapacitySnapshot(snap))
		},
	}
}

// fetchFleetCapacitySnapshot dials fleet.capacity through the daemon's
// unix socket, following dialFleetJobsClient's established no-daemon
// refusal (fleet_jobs.go) — there is no embedded path, per this file's
// header note on FleetSnapshot being daemon-owned in-memory state.
func fetchFleetCapacitySnapshot(ctx context.Context, deps fleetSessionsDeps) (capacity.FleetSnapshot, error) {
	c, err := dialFleetJobsClient(ctx, deps)
	if err != nil {
		return capacity.FleetSnapshot{}, err
	}
	var snap capacity.FleetSnapshot
	if err := c.Do(ctx, capacity.MethodFleetCapacity, nil, &snap); err != nil {
		return capacity.FleetSnapshot{}, err
	}
	return snap, nil
}

// fleetCapacitySnapshot is capacity.FleetSnapshot with a String() method
// for the human table — a defined type (not an alias) so this file can
// attach String without touching snapshot.go, and so output.Writer.Result
// still marshals the identical underlying struct/json tags in --json mode.
type fleetCapacitySnapshot capacity.FleetSnapshot

// String renders the human table: PROVIDER, STATE, and the three bucket
// usage percentages plus RESET_IN (see this file's header, deviation 4,
// for why TIER is not a column). An empty snapshot prints the header row
// only, never panics.
func (s fleetCapacitySnapshot) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "PROVIDER\tSTATE\tINTERACTIVE_USAGE%%\tAGENT_SDK_CREDIT%%\tAPI_CREDIT%%\tRESET_IN\n")
	for _, name := range sortedProviderNames(s.Providers) {
		slot := s.Providers[name]
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			name, slot.State,
			bucketPct(slot, capacity.BucketInteractiveUsage),
			bucketPct(slot, capacity.BucketAgentSDKCredit),
			bucketPct(slot, capacity.BucketAPICredit),
			bucketResetIn(slot, capacity.BucketInteractiveUsage),
		)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// sortedProviderNames returns providers' keys sorted for deterministic
// table row order (map iteration order is randomized in Go).
func sortedProviderNames(providers map[string]capacity.ProviderSlot) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// bucketPct renders one bucket's five-hour utilization as a percentage
// string, or "unknown" when the bucket is absent or carries the
// WindowUtilizationUnknown sentinel — never a bare 0, which would read as
// "confirmed empty" (snapshot.go's own documented distinction).
func bucketPct(slot capacity.ProviderSlot, kind capacity.BucketKind) string {
	bucket, ok := slot.Buckets[kind]
	if !ok || bucket.FiveHour.UtilizationPct == capacity.WindowUtilizationUnknown {
		return "unknown"
	}
	return fmt.Sprintf("%.1f", bucket.FiveHour.UtilizationPct)
}

// bucketResetIn renders one bucket's five-hour ResetsIn duration (already
// computed by the compositor against its own injected clock — this
// command never calls time.Now itself), or "-" when the bucket is absent.
func bucketResetIn(slot capacity.ProviderSlot, kind capacity.BucketKind) string {
	bucket, ok := slot.Buckets[kind]
	if !ok || bucket.FiveHour.ResetsIn <= 0 {
		return "-"
	}
	return bucket.FiveHour.ResetsIn.String()
}
