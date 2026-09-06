// Purpose: `cascade vault audit`, the read-only surface over what the
//
//	vault holds: entry names and kinds, stored OAuth grants and their
//	expiry, and the quarantine queue's depth.
//
// Inputs: cobra flags and the shared vaultDeps.
// Outputs: a human table by default, the versioned envelope under --json.
// Constraints: read-only, non-interactive and idempotent. No path here
//
//	can reach a stored value, and no reveal-style flag exists on this
//	surface: reading a value is the elevated `cascade vault get` verb and
//	stays there.
//
// SPORT: cmd/cascade/vault (ADD - audit).

package main

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newVaultAuditCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "audit",
		Short: "Report what the vault holds, without reading any value",
		Long: "Report this vault's contents and health.\n\n" +
			"Lists every stored entry by NAME and kind, every stored OAuth grant with\n" +
			"its expiry, and how many detections are waiting in the quarantine queue.\n" +
			"No value is read or printed on any path, and this command has no flag\n" +
			"that would print one: reading a value is the elevated `cascade vault get`\n" +
			"verb. Per-entry metadata (provider, last used, never resolved) is not\n" +
			"available in this build, and the report says so rather than guessing.",
		Example:     "  cascade vault audit\n  cascade vault audit --json",
		Args:        usageArgs(cobra.NoArgs),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runVaultAudit(cmd, deps)
		},
	}
}

// runVaultAudit builds the report and writes it.
func runVaultAudit(cmd *cobra.Command, deps vaultDeps) error {
	broker, err := vaultBroker(deps)
	if err != nil {
		return err
	}
	quarantine, err := vaultQuarantineStore(deps)
	if err != nil {
		return err
	}
	report, err := secrets.BuildAuditReport(cmd.Context(), secrets.AuditDeps{
		Broker:     broker,
		Quarantine: quarantine,
		Now:        runtime.NewSystemClock().Now,
	})
	if err != nil {
		return err
	}
	return vaultOutputWriter(cmd).Result(report)
}

// vaultQuarantineStore opens the quarantine ledger through the injected
// provider, refusing rather than proceeding when none is configured: an
// audit that silently reported a depth of zero would read as "nothing is
// waiting" on a host whose ledger it never opened.
func vaultQuarantineStore(deps vaultDeps) (*secrets.QuarantineStore, error) {
	if deps.Quarantine != nil {
		return deps.Quarantine()
	}
	if deps.Paths == nil {
		return nil, cascade.New(cascade.KindInternal,
			"vault: no quarantine ledger provider is configured")
	}
	dir := deps.Paths.DataDir()
	if dir == "" {
		return nil, cascade.New(cascade.KindUnavailable,
			"vault: could not resolve the cascade data directory")
	}
	return secrets.NewQuarantineStore(filepath.Join(dir, quarantineDirName), runtime.NewSystemClock())
}
