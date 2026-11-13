// Package docket provides utilities for mapping cards to projects by folder ownership.
package docket

import (
	"path/filepath"
	"strings"
)

// Card represents a card with a folder path.
type Card struct {
	Folder string
}

// Project represents a project that may own folders.
type Project struct {
	Name       string
	Folder     string
	HasTickets bool
}

// ResolveOwner maps each card's folder to the owning project's name.
// A plan (HasTickets true) owns its whole repository folder, so any card
// path under that folder maps to that project. A legacy pointer (HasTickets false)
// owns only its exact folder. Paths outside every owned folder are absent from
// the returned map. Paths are cleaned before comparing and matched on whole
// path segments.
func ResolveOwner(cards []Card, projects []Project) (map[string]string, error) {
	result := make(map[string]string)

	for _, card := range cards {
		cleanCard := filepath.Clean(card.Folder)

		var owner string
		found := false

		for _, proj := range projects {
			cleanProj := filepath.Clean(proj.Folder)

			if proj.HasTickets {
				if isUnderOrEqual(cleanCard, cleanProj) {
					owner = proj.Name
					found = true
					break
				}
			} else {
				if cleanCard == cleanProj {
					owner = proj.Name
					found = true
					break
				}
			}
		}

		if found {
			result[card.Folder] = owner
		}
	}

	return result, nil
}

// isUnderOrEqual reports whether child is equal to parent or is a descendant
// of parent using whole path segment matching.
func isUnderOrEqual(child, parent string) bool {
	if child == parent {
		return true
	}
	prefix := parent
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(child, prefix)
}
