// Purpose: the registry's data shapes — Entity (one deduplicated
//
//	SPORT-tagged thing) and Site (one place it was declared).
//
// Inputs: none — this file only declares shape.
// Outputs: Entity, Site, Registry.
// Constraints: JSON field order is fixed (struct field order) so the
//
//	tracked registry.json artifact renders deterministically; never a map
//	at the top level of Entity/Site.
//
// SPORT: internal.inventory.sport.Entity/ADDED, internal.inventory.sport.Registry/ADDED.

package sport

// StatusUnspecified marks an Entity or Site whose marker text carried no
// recognized status verb (ADD/ADDED/CHANGE/CHANGED/CHG/REMOVE/REMOVED/
// DEPRECATE/DEPRECATED). Never silently dropped — see doc.go.
const StatusUnspecified = ""

// Site is one file/line that declared an entity.
type Site struct {
	// File is the repo-relative path (matching git ls-files' own form).
	File string `json:"file"`
	Line int    `json:"line"`
	// Status is this site's normalized status verb, or StatusUnspecified.
	Status string `json:"status"`
	// Ticket is the ticket id found in this site's marker text, or "".
	Ticket string `json:"ticket,omitempty"`
	// Raw is the exact marker text this site parsed, after "SPORT:" and
	// before any splitting — provenance for a human auditing a merge.
	Raw string `json:"raw"`
}

// Entity is one deduplicated SPORT-tagged thing: a name declared at one or
// more Sites.
type Entity struct {
	// Name is the entity's identifier as stated in its marker text,
	// trimmed of separators — the dedup key.
	Name string `json:"name"`
	// Sites is every declaration site, sorted by File then Line.
	Sites []Site `json:"sites"`
	// Statuses is the sorted, deduplicated set of distinct statuses
	// found across Sites — one entry for a consistent history, more than
	// one when the same entity was declared with different statuses at
	// different sites (normal history, e.g. ADD then CHANGED; see
	// registry.go for what actually counts as a reportable
	// contradiction).
	Statuses []string `json:"statuses"`
}

// SiteCount returns len(e.Sites), the "how many places" figure a human
// glance at an entity most often wants.
func (e Entity) SiteCount() int { return len(e.Sites) }
