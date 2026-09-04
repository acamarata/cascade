// Purpose: `cascade vault audit`, the read surface over vault access
//
//	events.
//
// Inputs: cobra flags and the shared vaultDeps.
// Outputs: a typed refusal, today. The append-only audit log the events
//
//	would be read from is owned by the audit domain and is not yet
//	recorded, so this command reports that plainly.
//
// Constraints: it refuses rather than printing an empty event list. An
//
//	empty list is an assertion - "nothing accessed this vault" - and this
//	build cannot make that assertion truthfully. Printing one would be the
//	most misleading possible answer for a security surface.
//
// SPORT: cmd/cascade/vault (ADD - audit).

package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

func newVaultAuditCmd(_ vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "audit",
		Short: "Show vault access events from the audit log",
		Long: "Report the recorded accesses to this vault.\n\n" +
			"Vault access events are written to the append-only audit log. Until that\n" +
			"log is recorded, this command reports that the record is unavailable\n" +
			"rather than printing an empty list, which would read as a claim that\n" +
			"nothing has accessed the vault.",
		Example:     "  cascade vault audit\n  cascade vault audit --json",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(_ *cobra.Command, _ []string) error {
			return cascade.New(cascade.KindUnavailable,
				"vault: the vault access log is not recorded by this build, so there are no events to report; "+
					"this command refuses rather than printing an empty list, which would read as \"nothing accessed the vault\"")
		},
	}
}
