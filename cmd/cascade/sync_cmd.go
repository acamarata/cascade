// Purpose: `cascade sync` (07-CLI-COMMAND-TREE §sync) — status, run,
//   conflicts list and conflicts resolve, as thin mirrors of the sync.*
//   RPC methods over the S-38.T1 engine and the S-38.T2 conflict journal.
// Inputs: cobra args/flags; syncDeps injected at construction so a test
//   never touches a real store, a real keychain or a real peer.
// Outputs: process output through internal/output.Writer.
// Constraints: `conflicts resolve --keep local` DISCARDS the server's
//   copy, so it is an elevated verb — chosen by flag, never by a
//   blocking prompt, refused outright under CASCADE_NO_INPUT=1 without
//   --yes, and refused on Windows (tier-2). The read verbs are the pair
//   the generated MCP subset carries; resolve is deliberately not one.
// SPORT: cli.sync/ADD (P1-E17-W4-S38-T3).

package main

import (
	"context"
	goruntime "runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/storage"
	syncpkg "github.com/acamarata/cascade/internal/sync"
	"github.com/acamarata/cascade/pkg/cascade"
)

// syncDeps carries sync_cmd.go's external inputs.
type syncDeps struct {
	// Engine holds the conflict journal and the cursors. A test builds an
	// isolated one directly; production supplies OpenEngine instead.
	Engine *syncpkg.Engine
	// OpenEngine builds the engine when a verb actually runs, and is how
	// production supplies one. It is a function rather than a value
	// because building the real engine opens a database, and the command
	// tree is CONSTRUCTED for every invocation of this binary — including
	// `--help` and `cascade version`. Nil is valid when Engine is set.
	OpenEngine func() *syncpkg.Engine
	// PeerTier is the trust tier eligibility is answered for.
	PeerTier nodes.Tier
	// Gate authorizes an elevated resolution. Nil refuses rather than
	// allows — see the RPC surface's own note.
	Gate syncpkg.ElevationGate
	// Run performs one domain's sync. Nil makes `sync run` report that it
	// is not wired rather than reporting a sync that never happened.
	Run func(ctx context.Context, domain storage.DomainID, subkind string) error
	// Getenv reads CASCADE_NO_INPUT. Nil reads as unset.
	Getenv func(string) string
	// GOOS is the platform the tier-2 refusal is decided on. Empty reads
	// as this build's own.
	GOOS string
}

// mountSyncCmd attaches the `sync` command tree.
func mountSyncCmd(root *cobra.Command) {
	cmd := newSyncCmd(productionSyncDeps())
	guardUnknownSubcommands(cmd)
	root.AddCommand(cmd)
}

// newSyncCmd builds the sync noun and its four verbs.
func newSyncCmd(deps syncDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Replicate domains between this device and its peers",
		Long: "Report what syncs, run a sync, and settle the conflicts a merge had to\n" +
			"decide.\n\n" +
			"`sync status` lists every registered domain — including the ones this\n" +
			"peer's trust tier may not sync, because \"why is memory not syncing?\" is\n" +
			"not answered by omitting memory.\n\n" +
			"`sync conflicts resolve --keep local` DISCARDS the server's copy of a\n" +
			"record and is an elevated verb. The side to keep is a flag, never a\n" +
			"prompt, so a non-interactive run can make the choice; under\n" +
			"CASCADE_NO_INPUT=1 it additionally requires --yes.",
		Annotations: map[string]string{"local": "true"},
	}
	cmd.AddCommand(newSyncStatusCmd(deps), newSyncRunCmd(deps), newSyncConflictsCmd(deps))
	return cmd
}

// newSyncStatusCmd builds `sync status`.
func newSyncStatusCmd(deps syncDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "status",
		Short:       "Show what syncs, where each domain has got to, and how many conflicts are open",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := deps.surface().Status(cmd.Context())
			if err != nil {
				return err
			}
			return vaultOutputWriter(cmd).Result(syncStatusView{res})
		},
	}
}

// newSyncRunCmd builds `sync run`.
func newSyncRunCmd(deps syncDeps) *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Sync one domain, or every domain this peer's tier permits",
		Long: "With no --domain, every domain this peer's tier permits is synced.\n" +
			"One domain's failure does not stop the others: a sync that abandoned\n" +
			"four healthy domains because the fifth's remote was down would make a\n" +
			"whole fleet wait on one machine. Each domain's outcome is reported.",
		Example:     "  cascade sync run\n  cascade sync run --domain config --json",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := deps.surface().Run(cmd.Context(), domain)
			if err != nil {
				return err
			}
			return vaultOutputWriter(cmd).Result(syncRunView{res})
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "sync only this domain (default: every permitted domain)")
	return cmd
}

// newSyncConflictsCmd builds the `conflicts` sub-noun.
func newSyncConflictsCmd(deps syncDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "conflicts",
		Short:       "List and settle the conflicts a merge had to decide",
		Annotations: map[string]string{"local": "true"},
	}
	cmd.AddCommand(newSyncConflictsListCmd(deps), newSyncConflictsResolveCmd(deps))
	return cmd
}

// newSyncConflictsListCmd builds `sync conflicts list`.
func newSyncConflictsListCmd(deps syncDeps) *cobra.Command {
	return &cobra.Command{
		Use:         "list",
		Short:       "List every conflict a merge journaled",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := deps.surface().Conflicts(cmd.Context())
			if err != nil {
				return err
			}
			return vaultOutputWriter(cmd).Result(syncConflictsView{res})
		},
	}
}

// newSyncConflictsResolveCmd builds `sync conflicts resolve`.
func newSyncConflictsResolveCmd(deps syncDeps) *cobra.Command {
	var keep string
	var yes bool
	cmd := &cobra.Command{
		Use:   "resolve <record-id>",
		Short: "Settle one journaled conflict",
		Long: "--keep server accepts what the merge already decided and changes\n" +
			"nothing. --keep local DISCARDS the server's copy, which overrides the\n" +
			"authority a server-primary domain is defined by: it is an elevated verb,\n" +
			"it is refused on Windows (tier-2), and under CASCADE_NO_INPUT=1 it\n" +
			"additionally requires --yes, because a machine that cannot ask must be\n" +
			"told explicitly rather than assume.",
		Example:     "  cascade sync conflicts resolve cfg-3 --keep server\n",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := deps.guardElevatedResolve(keep, yes); err != nil {
				return err
			}
			res, err := deps.surface().Resolve(cmd.Context(), args[0], keep)
			if err != nil {
				return err
			}
			return vaultOutputWriter(cmd).Result(syncResolveView{res})
		},
	}
	cmd.Flags().StringVar(&keep, "keep", "",
		"which side to keep: `server` (what the merge decided) or `local` (discards the server's copy)")
	cmd.Flags().BoolVar(&yes, "yes", false,
		"confirm an elevated resolution without a prompt; required under CASCADE_NO_INPUT=1")
	return cmd
}

// guardElevatedResolve applies the two CLI-side gates on a discarding
// resolution, before the RPC surface's own elevation gate.
//
// Both are refusals rather than prompts. The side to keep is already a
// flag, so a caller has said what they want; what is left is whether this
// machine may act on it, and neither a locked-down platform nor a
// non-interactive run is a question a prompt could settle.
func (d syncDeps) guardElevatedResolve(keep string, yes bool) error {
	if keep != syncpkg.KeepLocal {
		return nil
	}
	if d.goos() == "windows" {
		return elevation.ErrWindowsTier2()
	}
	if d.Getenv != nil && d.Getenv("CASCADE_NO_INPUT") == "1" && !yes {
		return cascade.New(cascade.KindElevationRequired,
			"sync conflicts resolve --keep local discards the server's copy and requires --yes "+
				"when CASCADE_NO_INPUT=1; no prompt was attempted")
	}
	return nil
}

// goos reports the platform this refusal is decided on.
func (d syncDeps) goos() string {
	if d.GOOS != "" {
		return strings.ToLower(d.GOOS)
	}
	return goruntime.GOOS
}
