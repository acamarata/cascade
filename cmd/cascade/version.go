// Purpose: `cascade version` — prints the ldflags build stamp and install channel.
// Inputs:  internal/buildinfo's package vars, set at build time via
//
//	`-ldflags -X .../internal/buildinfo.<Name>=...`; unset in a plain
//	`go build` (dev builds print the documented dev defaults).
//
// Outputs: a human-readable line set containing version, commit, date, and
//
//	the §D-33 install channel as internal/update.ResolveChannel resolves it
//	(stamp, then Caskroom path, then install receipt, then manual).
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
	"github.com/acamarata/cascade/internal/update"
	"github.com/acamarata/cascade/pkg/cascade"
)

// versionEnv is the channel input `cascade version` resolves; tests replace it.
var versionEnv = productionUpdateEnv

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cascade version, commit, build date, and install channel",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			channel, _ := update.ResolveChannel(versionEnv())
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"cascade version %s\ncommit:  %s\nbuilt:   %s\nchannel: %s\n",
				buildinfo.Version, buildinfo.Commit, buildinfo.Date, channel)
			if err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, "write version output")
			}
			return nil
		},
	}
}
