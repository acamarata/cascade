// Purpose: the human-mode render for `cascade doctor sport`, matching
//
//	internal/inventory/report.go's Report.String() one-fact-per-line
//	convention.
//
// SPORT: internal.inventory.sport.Registry.String/ADDED.

package sport

import "fmt"

// String renders one summary line plus one line per entity: its name,
// status set, and site count. Kept deliberately terse (no per-site
// file/line breakdown) since that detail is exactly what --json carries
// in full for a script; the human table answers "what entities and what
// status", not "show me everything".
func (r Registry) String() string {
	out := fmt.Sprintf(
		"sport registry (as of %s): %d entities, %d sites, %d unspecified, %d multi-status\n",
		r.GeneratedAt, len(r.Entities), r.TotalSites, r.UnspecifiedCount, r.MultiStatusCount,
	)
	for _, e := range r.Entities {
		out += fmt.Sprintf("  %-50s %-24v sites=%d\n", e.Name, e.Statuses, e.SiteCount())
	}
	return out
}
