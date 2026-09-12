// Purpose: `cascade fleet jobs list|show|cancel|retry` (07-CLI-COMMAND-TREE
//
//	§Round-16 R-16.21) — each verb dials job.list/show/cancel/retry over
//	the daemon's unix socket via the generic internal/client.Client.Do,
//	following dialFleetJournal's exact daemonless-refusal pattern.
//
// CONTRACT NOTE: cmd/cascade must not import internal/rpc to DIAL the
// daemon (the cmd-rpc-server-boundary rule; use internal/client.Client
// instead). This file therefore declares its OWN local wire-shape types
// (jobRow/jobPageWire/etc. below) mirroring internal/rpc's
// JobRecord/JobPage/JobListQuery field-for-field, rather than importing
// those types — client.Do only needs `encoding/json`-compatible shapes on
// either side of the wire, so two independently-declared, field-matching
// structs round-trip identically without a shared import.
//
// Inputs: cobra args/flags; fleetSessionsDeps (Paths/DialContext), the
//
//	SAME injected deps newFleetCmd already threads through every other
//	fleet subcommand.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure.
//
// Constraints: read-only verbs (list/show) never mutate; cancel/retry
//
//	round-trip through the daemon's own outbox-recorded effect handlers
//	(internal/rpc/jobs_effects.go) — this file adds no client-side
//	idempotency logic of its own.
//
// SPORT: cmd/cascade/fleet-jobs-cli (ADD, P1-E29-W6-S60-T1).
package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const fleetJobsDialTimeout = fleetSessionsDialTimeout

var errFleetJobsNoDaemon = cascade.New(cascade.KindUnavailable,
	"cascade fleet jobs: no daemon socket reachable; start it with `cascade daemon run`")

// dialFleetJobsClient builds a *client.Client dialing the daemon,
// mirroring dialFleetJournal's exact daemonless-refusal pattern.
func dialFleetJobsClient(ctx context.Context, deps fleetSessionsDeps) (*client.Client, error) {
	st, ok := runtime.DaemonlessStateFrom(ctx)
	if !ok || st.Embedded {
		return nil, errFleetJobsNoDaemon
	}
	settings, err := daemon.ResolveSettings(nil, deps.Paths)
	if err != nil {
		return nil, err
	}
	return client.New(settings.SocketPath, client.DialFunc(deps.DialContext), fleetJobsDialTimeout), nil
}

// newFleetJobsCmd builds the `fleet jobs` command group.
func newFleetJobsCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "Inspect and control jobs (list, show, cancel, retry)",
	}
	cmd.AddCommand(newFleetJobsListCmd(deps))
	cmd.AddCommand(newFleetJobsShowCmd(deps))
	cmd.AddCommand(newFleetJobsCancelCmd(deps))
	cmd.AddCommand(newFleetJobsRetryCmd(deps))
	return cmd
}

// jobListParamsWire mirrors internal/rpc.JobListQuery's wire shape.
type jobListParamsWire struct {
	ScopeGlob string `json:"ScopeGlob,omitempty"`
	State     string `json:"State,omitempty"`
	Limit     int    `json:"Limit,omitempty"`
	Cursor    string `json:"Cursor,omitempty"`
}

// jobPageWire mirrors internal/rpc.JobPage's wire shape.
type jobPageWire struct {
	Jobs   []jobRow `json:"jobs"`
	Cursor string   `json:"cursor,omitempty"`
}

func newFleetJobsListCmd(deps fleetSessionsDeps) *cobra.Command {
	var scopeGlob, state, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List jobs, optionally filtered by scope glob or state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var page jobPageWire
			params := jobListParamsWire{ScopeGlob: scopeGlob, State: state, Limit: limit, Cursor: cursor}
			if err := c.Do(cmd.Context(), "job.list", params, &page); err != nil {
				return err
			}
			w := fleetSessionsOutputWriter(cmd)
			if page.Cursor != "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "next page cursor: %s\n", page.Cursor)
			}
			return w.Result(jobRows(page.Jobs))
		},
	}
	cmd.Flags().StringVar(&scopeGlob, "scope", "", "doublestar scope glob filter")
	cmd.Flags().StringVar(&state, "state", "", "job state filter")
	cmd.Flags().StringVar(&cursor, "cursor", "", "resume from a prior page's cursor")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum jobs to return (server default 50, cap 500)")
	return cmd
}

func newFleetJobsShowCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <job-id>",
		Short: "Show one job's full record",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var j jobRow
			if err := c.Do(cmd.Context(), "job.show", idParams{ID: args[0]}, &j); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(jobRows{j})
		},
	}
	return cmd
}

func newFleetJobsCancelCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a job (idempotent: already-terminal jobs succeed with no change)",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var result struct{}
			if err := c.Do(cmd.Context(), "job.cancel", idParams{ID: args[0]}, &result); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cancelling %s\n", args[0])
			return nil
		},
	}
	return cmd
}

func newFleetJobsRetryCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retry <job-id>",
		Short: "Retry a FAILED or REJECTED job as a new job row",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var j jobRow
			if err := c.Do(cmd.Context(), "job.retry", idParams{ID: args[0]}, &j); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(jobRows{j})
		},
	}
	return cmd
}

// idParams mirrors internal/rpc's own unexported idParams shape — this
// package cannot import that unexported type, and the wire shape
// ({"id": "..."}) is simple enough not to warrant an exported one there.
type idParams struct {
	ID string `json:"id"`
}

// jobRow is one rendered job.list/job.show row, mirroring
// internal/rpc.JobRecord's wire shape field-for-field.
type jobRow struct {
	ID                        string   `json:"id"`
	State                     string   `json:"state"`
	CreatedAt                 int64    `json:"created_at"`
	UpdatedAt                 int64    `json:"updated_at"`
	Capabilities              []string `json:"capabilities,omitempty"`
	MutableScope              string   `json:"mutable_scope"`
	RiskClass                 string   `json:"risk_class"`
	MinTaskClass              string   `json:"min_task_class"`
	NodeRequirements          string   `json:"node_requirements,omitempty"`
	TimeoutSeconds            int64    `json:"timeout_seconds"`
	CostCeiling               float64  `json:"cost_ceiling"`
	Priority                  int      `json:"priority"`
	ConsecutiveFailedAttempts int64    `json:"consecutive_failed_attempts"`
	ConsequenceClass          string   `json:"consequence_class"`
	DataClass                 string   `json:"data_class"`
}

type jobRows []jobRow

// String renders the table view.
func (rows jobRows) String() string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "ID\tSTATE\tRISK_CLASS\tPRIORITY\tCREATED_AT\n")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n", r.ID, r.State, r.RiskClass, r.Priority, r.CreatedAt)
	}
	_ = tw.Flush()
	return strings.TrimRight(buf.String(), "\n")
}
