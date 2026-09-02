// Purpose: `cascade version` — prints the ldflags build stamp and install channel.
// Inputs:  package-level vars set at build time via `-ldflags -X`; unset in a
//
//	plain `go build` (dev builds print the documented dev defaults).
//
// Outputs: a human-readable line set containing version, commit, date, and
//
//	the §D-33 install channel.
//
// Constraints: no business logic; A-T6 owns writing the ldflags/receipt stamp,
//
//	this ticket only defines the read side (R-14.108). installChannel must
//	resolve to "manual" whenever unstamped or stamped with an unknown value.
//
// SPORT: cmd/cascade — cobra-root, global-flags, version, completions.
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/pkg/cascade"
)

// These are the ldflags injection points. A-T6's build tooling sets them via
// e.g. `-X main.version=v2.0.0 -X main.commit=<sha> -X main.date=<rfc3339>
// -X main.installChannel=brew`. Left unset, a plain `go build` yields the dev
// defaults below.
var (
	version        = "dev"
	commit         = "none"
	date           = "unknown"
	installChannel = ""
)

// validInstallChannels are the §D-33 channel values install tooling may stamp.
var validInstallChannels = map[string]bool{
	"script":       true,
	"brew":         true,
	"oci":          true,
	"node-managed": true,
	"manual":       true,
}

// resolvedInstallChannel returns the stamped install channel, falling back to
// "manual" when unstamped or stamped with a value outside the §D-33 set.
func resolvedInstallChannel() string {
	if validInstallChannels[installChannel] {
		return installChannel
	}
	return "manual"
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cascade version, commit, build date, and install channel",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(),
				"cascade version %s\ncommit:  %s\nbuilt:   %s\nchannel: %s\n",
				version, commit, date, resolvedInstallChannel())
			if err != nil {
				return cascade.Wrap(cascade.KindUnavailable, err, "write version output")
			}
			return nil
		},
	}
}
