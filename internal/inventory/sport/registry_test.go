// SPORT: internal.inventory.sport.Dedupe/ADDED (tests).
package sport

import "testing"

func TestDedupe_GroupsMultipleSitesUnderOneEntity(t *testing.T) {
	raw := []SiteEntity{
		{entity: ParsedEntity{Name: "cmd/cascade/daemon", Status: "ADD"}, file: "a.go", line: 1},
		{entity: ParsedEntity{Name: "cmd/cascade/daemon", Status: "CHANGE"}, file: "b.go", line: 2},
	}
	reg := Dedupe(raw, "2026-01-01T00:00:00Z")
	if len(reg.Entities) != 1 {
		t.Fatalf("want 1 deduplicated entity, got %d: %+v", len(reg.Entities), reg.Entities)
	}
	e := reg.Entities[0]
	if e.Name != "cmd/cascade/daemon" || e.SiteCount() != 2 {
		t.Fatalf("got %+v", e)
	}
	if len(e.Statuses) != 2 {
		t.Fatalf("want 2 distinct statuses, got %v", e.Statuses)
	}
	if reg.MultiStatusCount != 1 {
		t.Fatalf("want MultiStatusCount 1, got %d", reg.MultiStatusCount)
	}
	if reg.TotalSites != 2 {
		t.Fatalf("want TotalSites 2, got %d", reg.TotalSites)
	}
}

func TestDedupe_UnspecifiedOnlyCounted(t *testing.T) {
	raw := []SiteEntity{
		{entity: ParsedEntity{Name: "cmd/cascade", Status: StatusUnspecified}, file: "a.go", line: 1},
	}
	reg := Dedupe(raw, "2026-01-01T00:00:00Z")
	if reg.UnspecifiedCount != 1 {
		t.Fatalf("want UnspecifiedCount 1, got %d", reg.UnspecifiedCount)
	}
	if reg.MultiStatusCount != 0 {
		t.Fatalf("want MultiStatusCount 0, got %d", reg.MultiStatusCount)
	}
}

func TestDedupe_SitesSortedDeterministically(t *testing.T) {
	raw := []SiteEntity{
		{entity: ParsedEntity{Name: "x", Status: "ADD"}, file: "z.go", line: 9},
		{entity: ParsedEntity{Name: "x", Status: "ADD"}, file: "a.go", line: 5},
		{entity: ParsedEntity{Name: "x", Status: "ADD"}, file: "a.go", line: 1},
	}
	reg := Dedupe(raw, "t")
	sites := reg.Entities[0].Sites
	if sites[0].File != "a.go" || sites[0].Line != 1 || sites[1].Line != 5 || sites[2].File != "z.go" {
		t.Fatalf("sites not sorted deterministically: %+v", sites)
	}
}

func TestDedupe_EntitiesSortedByName(t *testing.T) {
	raw := []SiteEntity{
		{entity: ParsedEntity{Name: "zebra", Status: "ADD"}, file: "a.go", line: 1},
		{entity: ParsedEntity{Name: "apple", Status: "ADD"}, file: "a.go", line: 2},
	}
	reg := Dedupe(raw, "t")
	if reg.Entities[0].Name != "apple" || reg.Entities[1].Name != "zebra" {
		t.Fatalf("entities not sorted by name: %+v", reg.Entities)
	}
}
