// Purpose: `cascade doctor sport` — the queryable SPORT entity registry
//
//	surface, sibling to `doctor counts` (internal/inventory/sport): "a
//	true queryable SPORT entity registry ... not [a] raw drift signal".
//	Unlike counts.SPORTLines, this lists deduplicated entities, each with
//	every declaration site, filterable by status and by a name substring.
//
// Inputs: none beyond cobra flags; reads the embedded registry.json via
//
//	sport.LoadRegistry — no live tree access, so this works identically in
//	an installed binary (matching `doctor counts`'s own embedded-artifact
//	pattern).
//
// Outputs: process output via internal/output.Writer; sport.Registry
//
//	(filtered), human table by default, the versioned --json envelope
//	with --json.
//
// Constraints: read-only. A stale registry.json is a drift-gate failure
//
//	(internal/build/sportgate.go), not this command's problem to detect.
//
// SPORT: cmd/cascade/doctor-sport/ADDED.
package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/inventory/sport"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newDoctorSportCmd builds `cascade doctor sport`. deps is accepted (and
// ignored) to match every other doctor subcommand constructor's shared
// signature at the newDoctorCmd call site — this view needs none of it
// today (LoadRegistry reads the embedded artifact, no live environment),
// but changing the shape the moment one view differs would make
// newDoctorCmd's mount list harder to scan, not easier.
func newDoctorSportCmd(_ doctorDeps) *cobra.Command {
	var status, name string
	cmd := &cobra.Command{
		Use:   "sport",
		Short: "List the deduplicated SPORT entity registry",
		Long: "Report every SPORT-tagged entity this tree declares, deduplicated by\n" +
			"name across every file that declares it, filterable by --status and\n" +
			"--name. This is the queryable registry: internal/inventory's\n" +
			"sport_lines field is a raw, non-deduplicated marker-line count, not\n" +
			"this.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctorSport(cmd, status, name)
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter to entities carrying this status (ADD, CHANGE, REMOVE, DEPRECATE)")
	cmd.Flags().StringVar(&name, "name", "", "filter to entities whose name contains this substring")
	return cmd
}

// runDoctorSport loads the registry, filters it, and renders it.
func runDoctorSport(cmd *cobra.Command, status, name string) error {
	reg, err := sport.LoadRegistry()
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "cascade doctor sport: load")
	}
	filtered := filterSportRegistry(reg, status, name)
	return doctorOutputWriter(cmd).Result(filtered)
}

// filterSportRegistry returns a copy of reg whose Entities match both
// filters (an empty filter always matches). Registry-level scalar fields
// (TotalSites, UnspecifiedCount, MultiStatusCount) are recomputed over
// the FILTERED entity set, never left describing the unfiltered whole,
// so a `--status ADD` result's counts describe only what it shows.
func filterSportRegistry(reg sport.Registry, status, name string) sport.Registry {
	out := sport.Registry{GeneratedAt: reg.GeneratedAt}
	for _, e := range reg.Entities {
		if !sportEntityMatches(e, status, name) {
			continue
		}
		out.Entities = append(out.Entities, e)
		out.TotalSites += e.SiteCount()
		if len(e.Statuses) == 1 && e.Statuses[0] == sport.StatusUnspecified {
			out.UnspecifiedCount++
		}
		if len(e.Statuses) > 1 {
			out.MultiStatusCount++
		}
	}
	return out
}

// sportEntityMatches reports whether e passes both filters.
func sportEntityMatches(e sport.Entity, status, name string) bool {
	if name != "" && !strings.Contains(e.Name, name) {
		return false
	}
	if status == "" {
		return true
	}
	for _, s := range e.Statuses {
		if strings.EqualFold(s, status) {
			return true
		}
	}
	return false
}
