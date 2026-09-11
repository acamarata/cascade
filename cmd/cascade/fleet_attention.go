// Purpose: `cascade fleet attention list|ack|open` (07-CLI-COMMAND-TREE
//
//	§fleet: "attention [list|ack|open] <- hidden top-level alias
//	`cascade attention`", R-14.92) — the human-facing door onto the
//	fleet.attention.list/get/ack JSON-RPC methods
//	(internal/fleet/supervision, via its Go client SDK,
//	internal/fleet/supervision.Client, D/S-07.T3).
//
// Inputs: cobra args/flags; the SAME fleetSessionsDeps injection fleet.go
//
//	already established for daemon-dial commands.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure. Never a bare fmt.Print.
//
// CONTRACT DEVIATION (scope attribution, recorded, not papered over).
// R-16.5's "defaults to the calling session's own scope chain" has no
// ambient mechanism to read from at the CLI layer either — no command in
// this tree resolves "the current session's scope" implicitly (grep for
// a CLI-side scope accessor found none; internal/context/scope's own
// resolver takes explicit ResolveInput everywhere). --scope-kind/
// --scope-id are this command's explicit statement of that scope,
// exactly mirroring internal/fleet/supervision/rpc.go's own
// own_scope request field; a future ticket that wires real session-scope
// resolution can default these flags without changing this command's
// verbs.
//
// SPORT: cmd/cascade/fleet (ADD, per T-1 sport_updates; attention half).
package main

import (
	"context"
	goruntime "runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fleetAttentionDialTimeout mirrors fleet.go's fleetSessionsDialTimeout.
const fleetAttentionDialTimeout = 5 * time.Second

// errAttentionNoDaemon mirrors errJournalNoDaemon: the attention queue is
// a daemon-owned domain with no embedded/offline equivalent.
var errAttentionNoDaemon = cascade.New(cascade.KindUnavailable,
	"cascade fleet attention: no daemon socket reachable; start it with `cascade daemon run`")

// errAttentionWindowsTier2 mirrors errBenchWindowsTier2: Windows tier-2
// has no daemon at all.
var errAttentionWindowsTier2 = cascade.New(cascade.KindUnsupported,
	"cascade fleet attention: unavailable on Windows tier-2 (no daemon)")

// mountFleetAttentionAlias registers the hidden top-level `cascade
// attention` alias for `cascade fleet attention`, mirroring
// mountFleetSessionsAlias's exact pattern (fleet.go).
func mountFleetAttentionAlias(root *cobra.Command, deps fleetSessionsDeps) {
	cmd := newFleetAttentionCmd(deps)
	cmd.Use = "attention"
	cmd.Hidden = true
	root.AddCommand(cmd)
}

// newFleetAttentionCmd builds the `attention` command group mounted
// under `fleet` (and, hidden, at the root).
func newFleetAttentionCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attention",
		Short: "Inspect the human-attention queue (list, ack, or open one item)",
	}
	cmd.AddCommand(newFleetAttentionListCmd(deps))
	cmd.AddCommand(newFleetAttentionAckCmd(deps))
	cmd.AddCommand(newFleetAttentionOpenCmd(deps))
	return cmd
}

// attentionScopeFlags binds --scope-kind/--scope-id to cmd. See this
// file's CONTRACT DEVIATION note on scope attribution.
func attentionScopeFlags(cmd *cobra.Command, scopeKind, scopeID *string) {
	cmd.Flags().StringVar(scopeKind, "scope-kind", string(scope.ScopeKindSession), "own scope kind for visibility resolution")
	cmd.Flags().StringVar(scopeID, "scope-id", "", "own scope id for visibility resolution")
}

// newFleetAttentionListCmd builds `fleet attention list [--all] [--kind
// K] [--include-acked]`.
func newFleetAttentionListCmd(deps fleetSessionsDeps) *cobra.Command {
	var all, includeAcked bool
	var kindFlag, scopeKind, scopeID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List attention-queue items (default: unack'd, own scope only)",
		Long: "List items awaiting human attention. The default view is unacknowledged\n" +
			"items within the caller's own scope chain (R-16.5). --all expands to\n" +
			"exactly the scopes the R-21.157 traversal table makes visible to the\n" +
			"caller - never an unfiltered global view.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var kindPtr *supervision.Kind
			if kindFlag != "" {
				k := supervision.Kind(kindFlag)
				if !k.Valid() {
					return cascade.Newf(cascade.KindInvalidInput, "cascade fleet attention list: %q is not a valid kind", kindFlag)
				}
				kindPtr = &k
			}
			own := supervision.ScopeRef{Kind: scope.Kind(scopeKind), ID: scopeID}
			c, err := dialFleetAttention(cmd.Context(), deps)
			if err != nil {
				return err
			}
			items, err := c.List(cmd.Context(), own, nil, all, kindPtr, includeAcked)
			if err != nil {
				return err
			}
			return fleetAttentionOutputWriter(cmd).Result(attentionRowsFrom(items))
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "expand to every traversal-table-visible scope, not just own")
	cmd.Flags().StringVar(&kindFlag, "kind", "", "filter to one kind (stall|elevation-refused|policy-ask|error)")
	cmd.Flags().BoolVar(&includeAcked, "include-acked", false, "include already-acknowledged items")
	attentionScopeFlags(cmd, &scopeKind, &scopeID)
	return cmd
}

// newFleetAttentionAckCmd builds `fleet attention ack <id>`.
func newFleetAttentionAckCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ack <id>",
		Short: "Acknowledge an attention-queue item",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialFleetAttention(cmd.Context(), deps)
			if err != nil {
				return err
			}
			item, err := c.Ack(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return fleetAttentionOutputWriter(cmd).Result(attentionRowFrom(item))
		},
	}
	return cmd
}

// newFleetAttentionOpenCmd builds `fleet attention open <id>`: prints the
// full item detail.
func newFleetAttentionOpenCmd(deps fleetSessionsDeps) *cobra.Command {
	var scopeKind, scopeID string
	cmd := &cobra.Command{
		Use:   "open <id>",
		Short: "Print an attention-queue item's full detail",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			own := supervision.ScopeRef{Kind: scope.Kind(scopeKind), ID: scopeID}
			c, err := dialFleetAttention(cmd.Context(), deps)
			if err != nil {
				return err
			}
			item, err := c.Get(cmd.Context(), args[0], own, nil)
			if err != nil {
				return err
			}
			return fleetAttentionOutputWriter(cmd).Result(attentionDetailFrom(item))
		},
	}
	attentionScopeFlags(cmd, &scopeKind, &scopeID)
	return cmd
}

// dialFleetAttention builds a supervision.Client dialing the daemon,
// refusing on Windows tier-2 or when no daemon is reachable.
func dialFleetAttention(ctx context.Context, deps fleetSessionsDeps) (*supervision.Client, error) {
	if goruntime.GOOS == "windows" {
		return nil, errAttentionWindowsTier2
	}
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return nil, errAttentionNoDaemon
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), fleetAttentionDialTimeout)
	return supervision.NewClient(c), nil
}

// fleetAttentionOutputWriter mirrors fleetJournalOutputWriter's exact
// pattern.
func fleetAttentionOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}
