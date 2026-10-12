package goldencheck

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Resolve maps every discovered root to its owner row, in sorted root order.
// A root listed in excluded (with a reason) is skipped. It fails, naming
// every offender, on an unmapped root, a stale or duplicate owner row, an
// exclusion without a reason or that also has an owner row, and a row with a
// missing field or an invalid regex.
func Resolve(roots []string, owners []Row, excluded map[string]string) ([]Row, error) {
	byRoot, problems := indexOwners(owners)
	present := make(map[string]bool, len(roots))
	sorted := append([]string(nil), roots...)
	sort.Strings(sorted)
	var rows []Row
	var unmapped []string
	for _, root := range sorted {
		present[root] = true
		row, owned := byRoot[root]
		reason, skipped := excluded[root]
		switch {
		case owned && skipped:
			problems = append(problems, fmt.Sprintf("root %s has both an owner row and an exclusion", root))
		case owned:
			rows = append(rows, row)
		case skipped && strings.TrimSpace(reason) == "":
			problems = append(problems, fmt.Sprintf("excluded root %s has no reason", root))
		case !skipped:
			unmapped = append(unmapped, root)
		}
	}
	if len(unmapped) > 0 {
		problems = append(problems, "golden root without an owner row or exclusion: "+strings.Join(unmapped, ", "))
	}
	problems = append(problems, staleEntries(present, byRoot, excluded)...)
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return rows, nil
}

// indexOwners validates each owner row and keys the table by root.
func indexOwners(owners []Row) (map[string]Row, []string) {
	byRoot := make(map[string]Row, len(owners))
	var problems []string
	for _, row := range owners {
		if row.Root == "" || row.Package == "" || row.RunRegex == "" {
			problems = append(problems, fmt.Sprintf("owner row %+v has an empty field", row))
			continue
		}
		if _, err := regexp.Compile(row.RunRegex); err != nil {
			problems = append(problems, fmt.Sprintf("owner row %s: bad regex: %v", row.Root, err))
			continue
		}
		if _, dup := byRoot[row.Root]; dup {
			problems = append(problems, "duplicate owner row for root "+row.Root)
			continue
		}
		byRoot[row.Root] = row
	}
	return byRoot, problems
}

// staleEntries names owner rows and exclusions whose root no longer exists.
func staleEntries(present map[string]bool, byRoot map[string]Row, excluded map[string]string) []string {
	var stale []string
	for root := range byRoot {
		if !present[root] {
			stale = append(stale, root)
		}
	}
	for root := range excluded {
		if !present[root] {
			stale = append(stale, root)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	sort.Strings(stale)
	return []string{"owner table names a root that does not exist: " + strings.Join(stale, ", ")}
}
