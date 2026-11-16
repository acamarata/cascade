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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

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
		Short: "List, show, promote or demote quota domains",
	}
	cmd.AddCommand(newFleetDomainsListCmd(deps))
	cmd.AddCommand(newFleetDomainsShowCmd(deps))
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

// fleetDomainsDo sends one fleet.domains.* call to the daemon; tests replace it.
var fleetDomainsDo = func(ctx context.Context, deps fleetSessionsDeps, method string, params, out any) error {
	c, err := dialFleetJobsClient(ctx, deps)
	if err != nil {
		return err
	}
	switch method {
	case "fleet.domains.list":
		return c.Do(ctx, "fleet.domains.list", params, out)
	case "fleet.domains.show":
		return c.Do(ctx, "fleet.domains.show", params, out)
	}
	return cascade.New(cascade.KindInvalidInput, "fleet domains: unknown method "+method)
}

// topologyEnvelopeWire mirrors the daemon's versioned fleet topology result.
type topologyEnvelopeWire struct {
	Version string          `json:"version"`
	Data    json.RawMessage `json:"data"`
}

// domainBucketWire mirrors one quota bucket of a fleet.domains result.
type domainBucketWire struct {
	Name              string    `json:"name"`
	Window            string    `json:"window"`
	Source            string    `json:"source"`
	Limit             int64     `json:"limit"`
	RemainingFraction float64   `json:"remaining_fraction"`
	Confidence        float64   `json:"confidence"`
	ResetAt           time.Time `json:"reset_at"`
	ObservedAt        time.Time `json:"observed_at"`
}

// domainWire mirrors one quota domain of a fleet.domains result.
type domainWire struct {
	ID          string             `json:"id"`
	Kind        string             `json:"kind"`
	BillingTier string             `json:"billing_tier"`
	Quarantined bool               `json:"quarantined"`
	Holders     int                `json:"holders"`
	Buckets     []domainBucketWire `json:"buckets"`
}

// fleetDomainRows renders domains as a table; the empty list is the header only.
type fleetDomainRows []domainWire

// String renders the table view.
func (rows fleetDomainRows) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "ID\tKIND\tBILLING_TIER\tQUARANTINED\tHOLDERS\tBUCKETS\n")
	for _, d := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%d\t%d\n", d.ID, d.Kind, d.BillingTier, d.Quarantined, d.Holders, len(d.Buckets))
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// fleetDomainView renders one domain with its buckets.
type fleetDomainView domainWire

// String renders the table view.
func (d fleetDomainView) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "ID\tKIND\tBILLING_TIER\tQUARANTINED\tHOLDERS\n")
	_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%d\n", d.ID, d.Kind, d.BillingTier, d.Quarantined, d.Holders)
	_, _ = fmt.Fprintf(tw, "\nBUCKET\tWINDOW\tSOURCE\tLIMIT\tREMAINING\tCONFIDENCE\n")
	for _, b := range d.Buckets {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%.2f\t%.2f\n", b.Name, b.Window, b.Source, b.Limit, b.RemainingFraction, b.Confidence)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}

// callFleetDomains runs one read method and decodes the envelope's data into out.
func callFleetDomains(cmd *cobra.Command, deps fleetSessionsDeps, method string, params, out any) error {
	var env topologyEnvelopeWire
	if err := fleetDomainsDo(cmd.Context(), deps, method, params, &env); err != nil {
		return err
	}
	if env.Version != "1" {
		return cascade.Newf(cascade.KindUnavailable, "%s: unsupported result version %q", method, env.Version)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return cascade.Newf(cascade.KindUnavailable, "%s: malformed result", method)
	}
	return nil
}

// newFleetDomainsListCmd builds `fleet domains list`.
func newFleetDomainsListCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List quota domains",
		Example: "  cascade fleet domains list --json",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			rows := fleetDomainRows{}
			if err := callFleetDomains(cmd, deps, "fleet.domains.list", struct{}{}, &rows); err != nil {
				return err
			}
			if rows == nil {
				rows = fleetDomainRows{}
			}
			return fleetSessionsOutputWriter(cmd).Result(rows)
		},
	}
}

// newFleetDomainsShowCmd builds `fleet domains show <id>`.
func newFleetDomainsShowCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:     "show <id>",
		Short:   "Show one quota domain with its buckets",
		Example: "  cascade fleet domains show anthropic-api",
		Args:    usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d fleetDomainView
			if err := callFleetDomains(cmd, deps, "fleet.domains.show", map[string]string{"id": args[0]}, &d); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(d)
		},
	}
}
