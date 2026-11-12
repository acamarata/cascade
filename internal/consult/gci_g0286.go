// Package consult provides GCI block assembly utilities.
package consult

import (
	"errors"
	"strings"
)

// GCISection represents a single section within a GCI block.
type GCISection struct {
	// Name is the heading of the section.
	Name string
	// Body is the content of the section.
	Body string
	// Binding indicates whether this section must precede non-binding sections.
	Binding bool
}

// ErrBudgetExceeded is returned when the assembled output exceeds 1200 tokens.
var ErrBudgetExceeded = errors.New("gci block budget exceeded")

// ErrInvalidGCISection is returned when a section has blank name/body or duplicate names.
var ErrInvalidGCISection = errors.New("invalid gci section")

// AssembleGCIBlock assembles sections into a formatted GCI block string.
// Binding sections are placed first, preserving input order within each group.
// Each section is rendered as "# "+Name+"\n"+Body, joined by "\n\n".
// Returns "", ErrInvalidGCISection for blank/whitespace-only Name or Body or duplicate Names.
// Returns "", nil for nil/empty sections.
// The omissions parameter is accepted for interface compatibility but currently unused.
func AssembleGCIBlock(sections []GCISection, _ string) (string, error) {
	if len(sections) == 0 {
		return "", nil
	}

	seen := make(map[string]struct{}, len(sections))
	for _, s := range sections {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Body) == "" {
			return "", ErrInvalidGCISection
		}
		if _, exists := seen[s.Name]; exists {
			return "", ErrInvalidGCISection
		}
		seen[s.Name] = struct{}{}
	}

	var binding []GCISection
	var regular []GCISection
	for _, s := range sections {
		if s.Binding {
			binding = append(binding, s)
		} else {
			regular = append(regular, s)
		}
	}

	ordered := append(binding, regular...)

	parts := make([]string, len(ordered))
	for i, s := range ordered {
		parts[i] = "# " + s.Name + "\n" + s.Body
	}

	result := strings.Join(parts, "\n\n")

	tokens := (len(result) + 3) / 4
	if tokens > 1200 {
		return "", ErrBudgetExceeded
	}

	return result, nil
}
