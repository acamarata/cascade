// Purpose: `cascade fleet mode show|set <mode> [--ttl]` (07-CLI-COMMAND-
//
//	TREE §Round-21 additions, P1-E41-W9-S79-T2). Dials fleet.mode.show/set
//	through the daemon's unix socket via internal/client.Client,
//	mirroring fleet_jobs.go's dialFleetJobsClient/idParams pattern.
//
// Inputs: cobra args/flags; the SAME fleetSessionsDeps injection every
//
//	other fleet subcommand in this package uses.
//
// Outputs: process output via internal/output.Writer; a typed taxonomy
//
//	error on failure. Never a bare fmt.Print.
//
// Constraints: --json is the root's existing persistent global flag (07
//
//	§global-flags), matching fleet_capacity.go's own precedent -- no
//	command-local --json flag is added here. Output is identical under
//	CASCADE_NO_INPUT=1: neither verb ever prompts.
//
// CONTRACT NOTE: cmd/cascade must not import internal/rpc to dial the
// daemon (the cmd-rpc-server-boundary rule); this file declares its own
// local wire-shape types mirroring internal/fleet/economics.ModeEnvelope/
// ModeResultWire field-for-field, matching fleet_jobs.go's identical
// precedent.
//
// SPORT: cmd/cascade/fleet-mode-cli (ADD, P1-E41-W9-S79-T2).
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// modeEnvelopeWire/modeResultWire mirror
// internal/fleet/economics.ModeEnvelope/ModeResultWire's wire shape.
type modeEnvelopeWire struct {
	Version string         `json:"version"`
	Data    modeResultWire `json:"data"`
}

type modeResultWire struct {
	Mode             string `json:"mode"`
	Source           string `json:"source"`
	SetAt            string `json:"set_at,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	LifecycleStage   string `json:"lifecycle_stage"`
	LifecycleDefault string `json:"lifecycle_default"`
}

// String renders the human-readable one-line summary for both show and
// set (the two verbs share an identical result shape).
func (m modeResultWire) String() string {
	if m.ExpiresAt != "" {
		return fmt.Sprintf("mode=%s source=%s set_at=%s expires_at=%s", m.Mode, m.Source, m.SetAt, m.ExpiresAt)
	}
	if m.SetAt != "" {
		return fmt.Sprintf("mode=%s source=%s set_at=%s", m.Mode, m.Source, m.SetAt)
	}
	return fmt.Sprintf("mode=%s source=%s lifecycle_default=%s", m.Mode, m.Source, m.LifecycleDefault)
}

// fleetModeShowParams/fleetModeSetParams mirror the daemon's own params
// shapes.
type fleetModeShowParams struct {
	ProjectID      string `json:"project_id,omitempty"`
	LifecycleStage string `json:"lifecycle_stage,omitempty"`
}

type fleetModeSetParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Mode      string `json:"mode"`
	TTL       string `json:"ttl,omitempty"`
}

// newFleetModeCmd builds the `fleet mode` command group.
func newFleetModeCmd(deps fleetSessionsDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mode",
		Short: "Show or set the scheduler mode (discover|plan|build|crunch|integrate|verify|release|incident)",
	}
	cmd.AddCommand(newFleetModeShowCmd(deps))
	cmd.AddCommand(newFleetModeSetCmd(deps))
	return cmd
}

// newFleetModeShowCmd builds `fleet mode show`.
func newFleetModeShowCmd(deps fleetSessionsDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the project's current scheduler mode",
		Long: "Show the live explicit scheduler mode for this project, or the\n" +
			"lifecycle-derived default when no explicit mode is set. Requires a\n" +
			"running daemon (`cascade daemon run`). --json emits the same versioned\n" +
			"envelope every other cascade command does. Output is identical with\n" +
			"CASCADE_NO_INPUT=1: this command never prompts.",
		Example: "  cascade fleet mode show\n" +
			"  cascade fleet mode show --json",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var env modeEnvelopeWire
			if err := c.Do(cmd.Context(), "fleet.mode.show", fleetModeShowParams{}, &env); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(env.Data)
		},
	}
}

// newFleetModeSetCmd builds `fleet mode set <mode> [--ttl]`.
func newFleetModeSetCmd(deps fleetSessionsDeps) *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   "set <mode>",
		Short: "Set the project's scheduler mode",
		Long: "Set the project's explicit scheduler mode to one of discover, plan,\n" +
			"build, crunch, integrate, verify, release, incident. --ttl accepts a\n" +
			"Go duration (e.g. 2h) and is refused for every mode but incident,\n" +
			"which defaults to a 4h TTL and renews at most three consecutive times\n" +
			"per project per week. Requires a running daemon (`cascade daemon run`).\n" +
			"Output is identical with CASCADE_NO_INPUT=1: this command never prompts.",
		Example: "  cascade fleet mode set crunch\n" +
			"  cascade fleet mode set incident --ttl 2h\n" +
			"  cascade fleet mode set incident --json",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := dialFleetJobsClient(cmd.Context(), deps)
			if err != nil {
				return err
			}
			var env modeEnvelopeWire
			params := fleetModeSetParams{Mode: args[0], TTL: ttl}
			if err := c.Do(cmd.Context(), "fleet.mode.set", params, &env); err != nil {
				return err
			}
			return fleetSessionsOutputWriter(cmd).Result(env.Data)
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "", "renewal TTL for incident mode only (Go duration, e.g. 2h); refused for every other mode")
	return cmd
}
