// Purpose: `cascade fleet leases list|release` (07-CLI-COMMAND-TREE
//
//	§Round-16 R-16.21) — dials lease.list/lease.release over the daemon's
//	unix socket, following fleet_jobs.go's exact dial pattern and its
//	CONTRACT NOTE on why this file declares its own local wire types
//	rather than importing internal/rpc (the cmd-rpc-server-boundary
//	rule).
//
// SPORT: cmd/cascade/fleet-leases-cli (ADD, P1-E29-W6-S60-T1).
package main

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newFleetLeasesCmd builds the `fleet leases` command group.
func newFleetLeasesCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "leases",
		Short: "Inspect and release resource leases",
	}
	cmd.AddCommand(newFleetLeasesListCmd(deps))
	cmd.AddCommand(newFleetLeasesReleaseCmd(deps))
	return cmd
}

// leaseListParamsWire mirrors internal/rpc.LeaseListQuery's wire shape
// (an untagged struct, so its default JSON keys are its Go field names).
type leaseListParamsWire struct {
	ScopeGlob string `json:"ScopeGlob,omitempty"`
	Limit     int    `json:"Limit,omitempty"`
	Cursor    string `json:"Cursor,omitempty"`
}

// leasePageWire mirrors internal/rpc.LeasePage's wire shape.
type leasePageWire struct {
	Leases []leaseRow `json:"leases"`
	Cursor string     `json:"cursor,omitempty"`
}

func newFleetLeasesListCmd(deps fleetSessionsDeps) *cobra.Command {
	var scopeGlob, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List resource leases, optionally filtered by scope glob",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var page leasePageWire
			params := leaseListParamsWire{ScopeGlob: scopeGlob, Limit: limit, Cursor: cursor}
			if err := c.Do(cmd.Context(), "lease.list", params, &page); err != nil {
				return err
			}
			if page.Cursor != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "next page cursor: %s\n", page.Cursor)
			}
			return fleetSessionsOutputWriter(cmd).Result(leaseRows(page.Leases))
		},
	}
	cmd.Flags().StringVar(&scopeGlob, "scope", "", "doublestar scope glob filter")
	cmd.Flags().StringVar(&cursor, "cursor", "", "resume from a prior page's cursor")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum leases to return (server default 50, cap 500)")
	return cmd
}

func newFleetLeasesReleaseCmd(deps fleetSessionsDeps) *cobra.Command {
	var asJob string
	cmd := &cobra.Command{
		Use:   "release <lease-id>",
		Short: "Release a lease (releasing another job's lease requires elevation)",
		Long: "Release a lease by id (\"<repo_id>:<scope_glob>\", as printed by\n" +
			"`fleet leases list`). Releasing a lease this job does not hold is an\n" +
			"elevated verb (06-FORGE-SPEC.md \\u00a75.14): the daemon returns\n" +
			"ELEVATION_REQUIRED with a nonce, and this command does not itself\n" +
			"carry an attestation producer -- pass --as-job to release a lease\n" +
			"your OWN job holds, which is unelevated.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var result struct{}
			params := map[string]string{"id": args[0]}
			if asJob != "" {
				params["as_job"] = asJob
			}
			if err := c.Do(cmd.Context(), "lease.release", params, &result); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "released %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&asJob, "as-job", "", "release as this job id (unelevated when it is the lease's own holder)")
	return cmd
}

// leaseRow is one rendered lease.list row, mirroring
// internal/rpc.LeaseRecord's wire shape field-for-field.
type leaseRow struct {
	ID         string `json:"id"`
	RepoID     string `json:"repo_id"`
	ScopeGlob  string `json:"scope_glob"`
	Holder     string `json:"holder"`
	IssuedAt   int64  `json:"issued_at"`
	TTLSeconds int64  `json:"ttl_seconds"`
	RenewCount int64  `json:"renew_count"`
	Epoch      int64  `json:"epoch"`
	State      string `json:"state"`
}

type leaseRows []leaseRow

// String renders the table view.
func (rows leaseRows) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	// HOLDER is the job id (ResourceLease has no separate job_id column —
	// see internal/jobs/model.go's own doc comment: "Holder // job id").
	_, _ = fmt.Fprintf(tw, "ID\tSCOPE_GLOB\tHOLDER\tISSUED_AT\tTTL\tRENEW_COUNT\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\n", r.ID, r.ScopeGlob, r.Holder, r.IssuedAt, r.TTLSeconds, r.RenewCount)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}
