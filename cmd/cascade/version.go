// Purpose: `cascade version` — prints the ldflags build stamp and install channel.
// Inputs:  internal/buildinfo's package vars, set at build time via
//
//	`-ldflags -X .../internal/buildinfo.<Name>=...`; unset in a plain
//	`go build` (dev builds print the documented dev defaults).
//
// Outputs: a human-readable line set containing version, commit, date, and
//
//	the §D-33 install channel.
//
// Constraints: no business logic; A-T6 owns the ldflags/receipt stamp AND
//
//	the single source of truth for it (internal/buildinfo, R-14.116) — this
//	file only reads it (R-14.108, read literally per R-14.116b: no stamp
//	vars of its own). installChannel must resolve to "manual" whenever
//	unstamped or stamped with an unknown value; internal/buildinfo owns
//	that resolution too.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/buildinfo"
	"github.com/acamarata/cascade/pkg/cascade"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cascade version, commit, build date, and install channel",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"cascade version %s\ncommit:  %s\nbuilt:   %s\nchannel: %s\n",
				buildinfo.Version, buildinfo.Commit, buildinfo.Date, buildinfo.ResolvedInstallChannel())
			if err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, "write version output")
			}
			return nil
		},
	}
}
