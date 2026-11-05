// Purpose: `cascade fleet learn reset [--all] [--scope] [--task-class]
//
//	[--tier] [--json]` (P1-LRN-04). Dials learn.reset, which deletes the
//	matching observation rows through the daemon's resetter.
//
// Inputs: cobra flags only. Either --all or at least one filter is
// required; --all cannot be combined with a filter. A --scope value is
// validated with internal/learn.ParseScopeKey before any dial.
//
// Outputs: "deleted N rows changed=<bool>" in human mode; the versioned envelope in
// --json mode.
//
// Constraints: the reset is explicit by flag and never prompts, so
// CASCADE_NO_INPUT=1 does not change it. The row deletion itself lives in
// the daemon's resetter, never in this file.
//
// SPORT: cmd/cascade/fleet-learn-cli (ADD, P1-LRN-04).
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/learn"
	"github.com/acamarata/cascade/pkg/cascade"
)

// learnResetParams is the learn.reset request body.
type learnResetParams struct {
	ScopeKey  string `json:"scope_key,omitempty"`
	TaskClass string `json:"task_class,omitempty"`
	Tier      string `json:"tier,omitempty"`
	All       bool   `json:"all"`
}

// learnResetResult mirrors the learn.reset reply: {rows_deleted, changed}.
type learnResetResult struct {
	RowsDeleted int  `json:"rows_deleted"`
	Changed     bool `json:"changed"`
}

// String renders the reset outcome in one line.
func (r learnResetResult) String() string {
	return fmt.Sprintf("deleted %d rows changed=%t", r.RowsDeleted, r.Changed)
}

// learnResetFilters carries the parsed reset flags.
type learnResetFilters struct {
	All       bool
	ScopeKey  string
	TaskClass string
	Tier      string
}

// hasFilter reports whether any scoping flag was given.
func (f learnResetFilters) hasFilter() bool {
	return f.ScopeKey != "" || f.TaskClass != "" || f.Tier != ""
}

// validate refuses an unscoped reset, an --all combined with a filter, and
// a malformed scope key, all before any daemon dial.
func (f learnResetFilters) validate() error {
	if f.All && f.hasFilter() {
		return cascade.New(cascade.KindInvalidInput,
			"cascade fleet learn reset: --all cannot be combined with --scope, --task-class or --tier")
	}
	if !f.All && !f.hasFilter() {
		return cascade.New(cascade.KindInvalidInput,
			"cascade fleet learn reset: refusing an unscoped reset; pass --all or at least one of --scope, --task-class, --tier")
	}
	if f.ScopeKey != "" {
		if _, err := learn.ParseScopeKey(f.ScopeKey); err != nil {
			return err
		}
	}
	return nil
}

// newFleetLearnResetCmd builds `fleet learn reset`.
func newFleetLearnResetCmd(deps fleetSessionsDeps) *cobra.Command {
	var f learnResetFilters
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "Delete learned observations (all, or matching a scope, task class or tier)",
		Long: "Delete learned capability observations. Pass --all to reset every row,\n" +
			"or narrow the reset with --scope, --task-class and/or --tier (--all\n" +
			"cannot be combined with a filter). An unscoped reset is refused.\n" +
			"Requires a running daemon (`cascade daemon run`). Output is identical\n" +
			"with CASCADE_NO_INPUT=1: this command never prompts.",
		Example: "  cascade fleet learn reset --all\n" +
			"  cascade fleet learn reset --scope global --tier T1 --json",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := f.validate(); err != nil {
				return err
			}
			c, err := dialFleetLearnClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var out learnResetResult
			params := learnResetParams{ScopeKey: f.ScopeKey, TaskClass: f.TaskClass, Tier: f.Tier, All: f.All}
			if err := c.Do(cmd.Context(), learnResetMethod, params, &out); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(out)
		},
	}
	cmd.Flags().BoolVar(&f.All, "all", false, "reset every learned observation row")
	cmd.Flags().StringVar(&f.ScopeKey, "scope", "", "reset rows for one scope_key (repo:<id>, lang:<Language> or global)")
	cmd.Flags().StringVar(&f.TaskClass, "task-class", "", "reset rows for one task class")
	cmd.Flags().StringVar(&f.Tier, "tier", "", "reset rows for one capacity tier")
	return cmd
}
