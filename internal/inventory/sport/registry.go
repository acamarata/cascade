// Purpose: deduplicate scanned SPORT declarations by entity name into the
//
//	Registry both the CLI view and the tracked JSON artifact render.
//
// Inputs: the []SiteEntity slice ScanFiles produces.
// Outputs: Registry (Entities sorted by Name, each Entity's Sites sorted
//
//	by File then Line for deterministic JSON — Art.7.3's determinism
//	requirement applies to this artifact exactly as it does to counts.json).
//
// Constraints: never confuses "declared with a different status at a
//
//	different site" (normal history — see below) with a genuine
//	contradiction. This package draws NO such contradiction line at all:
//	Registry.MultiStatusCount only counts entities with more than one
//	distinct status, reported as a number, never a hard failure, because
//	ADD-then-CHANGED is the expected shape of an entity's own history and
//	this package has no ordering information (git blame timestamps, not
//	tracked here) to tell that apart from an actual inconsistency. See
//	internal/build/sportgate.go for where the one thing this package DOES
//	consider a hard failure (a malformed, unparseable line) is enforced.
//
// SPORT: internal.inventory.sport.Registry/ADDED, internal.inventory.sport.Dedupe/ADDED.

package sport

import "sort"

// Registry is the deduplicated entity set.
type Registry struct {
	GeneratedAt string   `json:"generated_at"`
	Entities    []Entity `json:"entities"`
	// TotalSites is the sum of every Entity's SiteCount — the raw
	// declaration-site total, comparable to internal/inventory's
	// SPORTLines (though not identical: one marker LINE can yield more
	// than one entity via top-level-comma splitting, so TotalSites can
	// exceed SPORTLines).
	TotalSites int `json:"total_sites"`
	// UnspecifiedCount is how many entities have StatusUnspecified as
	// their ONLY status across every site (see doc.go).
	UnspecifiedCount int `json:"unspecified_count"`
	// MultiStatusCount is how many entities were declared with more than
	// one distinct status across their sites — reported, not failed; see
	// this file's own doc comment for why.
	MultiStatusCount int `json:"multi_status_count"`
}

// Dedupe groups raw scanned entities by Name into a sorted Registry.
func Dedupe(raw []SiteEntity, generatedAt string) Registry {
	byName := map[string]*Entity{}
	var order []string
	for _, r := range raw {
		e, ok := byName[r.entity.Name]
		if !ok {
			e = &Entity{Name: r.entity.Name}
			byName[r.entity.Name] = e
			order = append(order, r.entity.Name)
		}
		e.Sites = append(e.Sites, Site{
			File:   r.file,
			Line:   r.line,
			Status: r.entity.Status,
			Ticket: r.entity.Ticket,
			Raw:    r.entity.Raw,
		})
	}
	sort.Strings(order)
	reg := Registry{GeneratedAt: generatedAt}
	for _, name := range order {
		e := finalizeEntity(*byName[name])
		reg.Entities = append(reg.Entities, e)
		reg.TotalSites += e.SiteCount()
		if len(e.Statuses) == 1 && e.Statuses[0] == StatusUnspecified {
			reg.UnspecifiedCount++
		}
		if len(e.Statuses) > 1 {
			reg.MultiStatusCount++
		}
	}
	return reg
}

// finalizeEntity sorts e's Sites deterministically and computes Statuses.
func finalizeEntity(e Entity) Entity {
	sort.Slice(e.Sites, func(i, j int) bool {
		if e.Sites[i].File != e.Sites[j].File {
			return e.Sites[i].File < e.Sites[j].File
		}
		return e.Sites[i].Line < e.Sites[j].Line
	})
	seen := map[string]bool{}
	for _, s := range e.Sites {
		if !seen[s.Status] {
			seen[s.Status] = true
			e.Statuses = append(e.Statuses, s.Status)
		}
	}
	sort.Strings(e.Statuses)
	return e
}
