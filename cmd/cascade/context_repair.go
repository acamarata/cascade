// Purpose: explicit daemon-only context maintenance with audited rebuilds.
package main

import (
	"encoding/json"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const contextRepairMethod = "context.repair"

// contextRepairTimeout bounds the whole repository rebuild round trip.
const contextRepairTimeout = 10 * time.Minute

type repairParams struct {
	Repo    string `json:"repo"`
	Rebuild bool   `json:"rebuild"`
	Reason  string `json:"reason,omitempty"`
}

// validateRepairReason runs at both boundaries before dialing or registering.
func validateRepairReason(rebuild bool, reason string) error {
	if rebuild && (utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 200) {
		return cascade.New(cascade.KindInvalidInput, "context.repair: --rebuild requires a reason of 1..200 runes")
	}
	if !rebuild && reason != "" {
		return cascade.New(cascade.KindInvalidInput, "context.repair: --reason requires --rebuild")
	}
	return nil
}

func newContextRepairCmd(deps contextScopeDeps) *cobra.Command {
	var params repairParams
	cmd := &cobra.Command{
		Use: "repair", Short: "Repair repository context through the daemon",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateRepairReason(params.Rebuild, params.Reason); err != nil {
				return err
			}
			ctx := cmd.Context()
			if st, ok := runtime.DaemonlessStateFrom(ctx); ok && st.Embedded {
				return cascade.New(cascade.KindUnavailable, "context.repair needs a running daemon. Start it with `cascade daemon start`.")
			}
			root, err := factsRepoPath(params.Repo)
			if err != nil {
				return err
			}
			params.Repo = root
			settings, err := daemon.ResolveSettings(nil, deps.Paths)
			if err != nil {
				return err
			}
			c := client.New(settings.SocketPath, client.DialFunc(deps.DialContext), contextRepairTimeout)
			var out json.RawMessage
			if err := c.Do(ctx, contextRepairMethod, params, &out); err != nil {
				return err
			}
			return contextScopeOutputWriter(cmd).Result(factsView{Raw: out})
		},
	}
	cmd.Flags().StringVar(&params.Repo, "repo", "", "repository path (defaults to the working directory)")
	cmd.Flags().BoolVar(&params.Rebuild, "rebuild", false, "discard and rebuild the repository indexes after auditing")
	cmd.Flags().StringVar(&params.Reason, "reason", "", "reason for rebuilding (1..200 runes)")
	return cmd
}
