// Purpose: `cascade vault import`, the vault.env shim: bulk-load a
//
//	KEY=value environment file into the vault.
//
// Inputs: a file path argument and the shared vaultDeps.
// Outputs: a report of how many entries were created and updated, plus the
//
//	names touched. Never a value.
//
// Constraints: import is idempotent - a second run of the same file exits 0
//
//	and overwrites in place, matching `vault set`'s non-interactive
//	behaviour. The file is parsed in full before the first write, so a
//	malformed file loads nothing. No line content ever reaches output.
//
// SPORT: cmd/cascade/vault (ADD - import).

package main

import (
	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// importResultView is `vault import`'s rendered result.
type importResultView struct {
	Parsed  int      `json:"parsed"`
	Created int      `json:"created"`
	Updated int      `json:"updated"`
	Names   []string `json:"names"`
	Backend string   `json:"backend"`
}

func newVaultImportCmd(deps vaultDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "import FILE",
		Short: "Import a vault.env file (KEY=value lines) into the vault",
		Long: "Load every KEY=value assignment in FILE into the vault.\n\n" +
			"Blank lines and # comments are skipped; a value may be quoted. A key that\n" +
			"already exists is updated in place, so re-running the same import is safe\n" +
			"and changes nothing. A line the parser cannot read stops the import before\n" +
			"anything is written, and the error names the line number only: the line\n" +
			"itself is withheld because it holds a secret.\n\n" +
			"The file is read once and never copied anywhere else.",
		Example:     "  cascade vault import ./vault.env\n  cascade vault import ./vault.env --json",
		Args:        usageArgs(cobra.ExactArgs(1)),
		Annotations: map[string]string{"local": "true"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if deps.ReadFile == nil {
				return cascade.New(cascade.KindInternal, "vault: no file reader is configured")
			}
			data, err := deps.ReadFile(args[0])
			if err != nil {
				return cascade.Wrapf(cascade.KindNotFound, err, "vault: could not read %q", args[0])
			}
			broker, err := vaultBroker(deps)
			if err != nil {
				return err
			}
			report, err := secrets.Import(cmd.Context(), broker, data)
			if err != nil {
				return err
			}
			names := report.Names
			if names == nil {
				names = []string{}
			}
			return vaultOutputWriter(cmd).Result(importResultView{
				Parsed:  report.Parsed,
				Created: report.Created,
				Updated: report.Updated,
				Names:   names,
				Backend: broker.Backend(),
			})
		},
	}
}
