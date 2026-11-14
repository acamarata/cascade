// Purpose: `cascade fleet domains promote <domain-id> --tier paid` and
//
//	`cascade fleet domains demote <domain-id>`: move one quota domain to
//	the paid billing tier on fresh daemon-side evidence, or back to
//	discover.
//
// Inputs: the domain id (positional), --tier, and the fleetSessionsDeps
//
//	socket seams plus the elevated-call seams (fleetDomainsCallDeps).
//
// Outputs: the domain, its new tier and each probed provider.
// Constraints: promote is dialed only through callElevated and refuses on a
//
//	platform without the local attestation flow; demote needs no
//	attestation; no credential is read or printed.
//
// SPORT: cmd/cascade fleet domains.
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fleetDomainsCallDeps builds the elevated-call collaborators promote signs
// with; tests inject a test custody here.
var fleetDomainsCallDeps = productionElevatedCallDeps

// newFleetDomainsCmd builds the `fleet domains` group.
func newFleetDomainsCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domains",
		Short: "Promote or demote a quota domain's billing tier",
	}
	cmd.AddCommand(newFleetDomainsPromoteCmd(deps))
	cmd.AddCommand(newFleetDomainsDemoteCmd(deps))
	return cmd
}

// newFleetDomainsPromoteCmd builds `fleet domains promote <domain-id> --tier paid`.
func newFleetDomainsPromoteCmd(deps fleetSessionsDeps) *cobra.Command {
	var tier string
	cmd := &cobra.Command{
		Use:   "promote <domain-id>",
		Short: "Promote a quota domain to the paid billing tier",
		Long: "The daemon proves every provider of the domain with one authenticated call\n" +
			"and then promotes it. This is an elevated verb: it needs a fresh local\n" +
			"attestation. Requires a running daemon.",
		Example: "  cascade fleet domains promote anthropic-api --tier paid",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if tier != tierPaid {
				return cascade.Newf(cascade.KindInvalidInput, "fleet domains promote: --tier must be %q", tierPaid)
			}
			if err := refuseElevatedOnUnsupportedPlatform("fleet domains promote"); err != nil {
				return err
			}
			c, err := dialFleetDomainsPromoteClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var reply domainChangeReply
			err = callElevated(cmd.Context(), c, fleetDomainsCallDeps(cmd.ErrOrStderr()), domainPromoteMethod,
				domainChangeParams{Domain: args[0], Tier: tierPaid}, &reply)
			if err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(reply)
		},
	}
	cmd.Flags().StringVar(&tier, "tier", "", "billing tier to promote to (paid)")
	return cmd
}

// newFleetDomainsDemoteCmd builds `fleet domains demote <domain-id>`.
func newFleetDomainsDemoteCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "demote <domain-id>",
		Short:   "Return a quota domain to the discover billing tier",
		Example: "  cascade fleet domains demote anthropic-api",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var reply domainChangeReply
			if err := c.Do(cmd.Context(), domainDemoteMethod, domainChangeParams{Domain: args[0], Tier: tierDiscover}, &reply); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(reply)
		},
	}
}

// dialFleetDomainsPromoteClient is dialFleetJobsClient's daemon client with
// the round trip bounded for every provider probe the promote may run.
func dialFleetDomainsPromoteClient(ctx context.Context, deps fleetSessionsDeps) (*client.Client, error) {
	if _, err := dialFleetJobsClient(ctx, deps); err != nil { // the same no-daemon refusal
		return nil, err
	}
	probes, err := pathProbeCount(ctx, deps.Paths)
	if err != nil {
		return nil, err
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), promoteRoundTripTimeout(probes)), nil
}

// pathProbeCount counts the providers in the providers.db under paths.
func pathProbeCount(ctx context.Context, paths runtime.PathProvider) (int, error) {
	db, err := openMigratedDB(ctx, filepath.Join(paths.DataDir(), providerRegistryDBFile),
		func(ctx context.Context, db *sql.DB) error {
			return registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, runtime.NewSystemClock(), "", "")
		})
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()
	return registryProbeCount(ctx, registry.NewRegistry(db, runtime.NewSystemClock()))
}
