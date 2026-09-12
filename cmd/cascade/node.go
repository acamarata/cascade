// Purpose: the `cascade node` CLI surface beyond `serve` (S-36.T2's own
//
//	mount): list/status/drain/remove/rotate-key/revoke, thin verbs over
//	internal/nodes' RecordStore. `enroll` is node_admit.go (this ticket's
//	other new file, split out for the 300-line cap and because it alone
//	needs the ssh-dial + elevation composition); `serve` stays mounted
//	by node_serve.go per R-16.56.
//
// Inputs: cobra args/flags; nodeCLIDeps injected so no test resolves the
//
//	real CASCADE_HOME or touches a real OS keystore (Art.7.1).
//
// Outputs: internal/output rendering (table or --json envelope); a typed
//
//	taxonomy error on any failure, mapped to its exit code by main.
//
// Constraints: 07-CLI-COMMAND-TREE.md §node mirror rule — every verb here
//
//	is a thin call into internal/nodes, never business logic of its own.
//	cmd/cascade may not import internal/rpc (the cmd-rpc-server boundary,
//	internal/client/boundary_test.go), so these verbs call RecordStore
//	methods directly rather than dispatching through an *rpc.Registry —
//	see drain.go's package doc for the full contradiction this follows.
//	remove/rotate-key/revoke are elevated (06 §5.14); rotate-key/revoke
//	are NOT in internal/rpc's canonical elevationTable (that file and its
//	spec cross-check test are out of this ticket's files_scope to extend
//	safely — see the journal), so their elevation is enforced directly
//	here via requireElevation rather than policy.IsDaemonlessElevationAllowed's
//	table lookup. R-21.226: every verb refuses on Windows tier-2 via the
//	SAME nodes.RefuseOnGOOS the serve verb already uses (R-16.60: no
//	second copy of that check).
//
// SPORT: cmd/cascade/node (CHG: list/status/drain/remove/rotate-key/revoke
//
//	ADDED; P1-E17-W4-S36-T4).
package main

import (
	"context"
	"fmt"
	"os"
	goruntime "runtime"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/policy"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// nodeCLIDeps carries every environment-touching input the node.go/node_admit.go
// verbs need, mirroring nodeServeDeps' established injection pattern
// (node_serve.go) so no test resolves the real CASCADE_HOME or touches a
// real OS keychain/keystore.
type nodeCLIDeps struct {
	Paths      cruntime.PathProvider
	Clock      cruntime.Clock
	SecretsDir string
	Gate       *elevationGate
	Getenv     cruntime.Getenv
	GOOS       string
	// Dialer, when non-nil, overrides nodes.NewSSHDialer for node_admit.go's
	// enroll verb (tests substitute a fake Dialer; production leaves this
	// nil and builds the real one from Keystore/self identity per call).
	Dialer nodes.Dialer
}

// productionNodeCLIDeps builds nodeCLIDeps against the real environment,
// mirroring productionNodeServeDeps/productionProviderDeps exactly.
func productionNodeCLIDeps() nodeCLIDeps {
	paths := lazyPaths{}
	getenv := os.Getenv
	return nodeCLIDeps{
		Paths: paths,
		Clock: cruntime.SystemClock{},
		Gate: newElevationGate(
			elevation.NewKeystore,
			func() elevation.Backend { return elevation.NewFileBackend(paths.DataDir()) },
			cruntime.NewSystemClock(), getenv,
		),
		Getenv: getenv,
		GOOS:   goruntime.GOOS,
	}
}

// openRecordStore resolves dataDir and opens the real file-backed
// RecordStore, the single construction path every verb in this file
// shares (mirrors composeNodeServe's dataDir resolution).
func openRecordStore(deps nodeCLIDeps) (*nodes.RecordStore, error) {
	dataDir := deps.Paths.DataDir()
	if dataDir == "" {
		return nil, cascade.New(cascade.KindUnavailable, "node: could not resolve the data directory")
	}
	return nodes.NewRecordStore(nodes.NewFileRecordBackend(dataDir), deps.Clock), nil
}

// requireElevation enforces the 06 §5.14 elevation flow for verb
// UNCONDITIONALLY (never consulting internal/rpc's elevationTable): used
// for node.rotate_key and node.revoke, which R-21.220 declares elevated
// but which are not (and, per this ticket's files_scope, cannot safely be
// made) members of that shared table — see this file's package doc.
// CASCADE_NO_INPUT=1 is a hard error, never a hang (06 §5.8/08 §2),
// exactly matching elevationGate.Authorize's own CASCADE_NO_INPUT branch.
func requireElevation(_ context.Context, g *elevationGate, verb string) error {
	if g.getenv != nil && g.getenv("CASCADE_NO_INPUT") == "1" {
		return cascade.Newf(cascade.KindElevationRequired,
			"node: %s needs local presence and CASCADE_NO_INPUT=1 forbids prompting for it", verb)
	}
	enrolled, available := g.preconditions()
	if !enrolled || !available {
		return policy.ErrElevationRequired(verb, enrolled, available)
	}
	return nil
}

// newNodeListCmd builds `cascade node list` (✦ read verb, never elevated).
func newNodeListCmd(deps nodeCLIDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List every enrolled node (read-only)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
				return err
			}
			store, err := openRecordStore(deps)
			if err != nil {
				return err
			}
			recs, err := store.List()
			if err != nil {
				return err
			}
			view := nodeListView{Nodes: make([]nodeRowView, 0, len(recs))}
			for _, r := range recs {
				view.Nodes = append(view.Nodes, newNodeRowView(r, deps.Clock))
			}
			return nodeOutputWriter(cmd).Result(view)
		},
	}
}

// newNodeStatusCmd builds `cascade node status <id>` (✦ read verb, never
// elevated).
func newNodeStatusCmd(deps nodeCLIDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "status NODE_ID",
		Short: "Show one node's device record, liveness and tunnel state",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
				return err
			}
			store, err := openRecordStore(deps)
			if err != nil {
				return err
			}
			rec, err := store.Get(args[0])
			if err != nil {
				return err
			}
			view := nodeStatusView{nodeRowView: newNodeRowView(rec, deps.Clock), Tunnel: "unknown (no in-process tunnel session)"}
			return nodeOutputWriter(cmd).Result(view)
		},
	}
}

// newNodeDrainCmd builds `cascade node drain <id>`: not elevated (a real
// capability-reducing action, not a destructive or secret-exposing one —
// 06 §5.14's elevated set is enroll/remove/rotate-key/revoke only).
func newNodeDrainCmd(deps nodeCLIDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "drain NODE_ID",
		Short: "Mark a node drained: stop placing new work on it",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
				return err
			}
			store, err := openRecordStore(deps)
			if err != nil {
				return err
			}
			rec, err := store.Drain(args[0])
			if err != nil {
				return err
			}
			return nodeOutputWriter(cmd).Result(newNodeRowView(rec, deps.Clock))
		},
	}
}

// nodeRemovedView is `node remove`'s result.
type nodeRemovedView struct {
	NodeID  string `json:"node_id"`
	Removed bool   `json:"removed"`
}

func (v nodeRemovedView) String() string { return fmt.Sprintf("%s removed", v.NodeID) }

// newNodeRemoveCmd builds `cascade node remove <id>` ⚠ — an elevated verb
// (06 §5.14, node.remove is already in internal/rpc's elevationTable).
func newNodeRemoveCmd(deps nodeCLIDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "remove NODE_ID",
		Short:       "Remove an enrolled node's device record (elevated)",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := nodes.RefuseOnGOOS(deps.GOOS); err != nil {
				return err
			}
			if err := deps.Gate.Authorize(cmd.Context(), "node.remove"); err != nil {
				return err
			}
			store, err := openRecordStore(deps)
			if err != nil {
				return err
			}
			if err := store.Remove(args[0]); err != nil {
				return err
			}
			return nodeOutputWriter(cmd).Result(nodeRemovedView{NodeID: args[0], Removed: true})
		},
	}
}

// mountNodeCLICmds attaches list/status/drain/remove/rotate-key/revoke/
// upgrade to the `node` group node_serve.go's mountNodeCmd already
// created (that function mounts `serve` alone, per its own doc comment).
// enroll is node_admit.go's newNodeAdmitCmd; upgrade is S-36.T5's
// node_upgrade.go newNodeUpgradeCmd.
func mountNodeCLICmds(cmd *cobra.Command, deps nodeCLIDeps) {
	cmd.AddCommand(newNodeAdmitCmd(deps))
	cmd.AddCommand(newNodeListCmd(deps))
	cmd.AddCommand(newNodeStatusCmd(deps))
	cmd.AddCommand(newNodeDrainCmd(deps))
	cmd.AddCommand(newNodeRemoveCmd(deps))
	cmd.AddCommand(newNodeRotateKeyCmd(deps))
	cmd.AddCommand(newNodeRevokeCmd(deps))
	cmd.AddCommand(newNodeUpgradeCmd(deps))
}
