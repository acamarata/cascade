// Package write provides ticket YAML parsing for conductor ingest.
package write

import (
	"errors"
	"strings"
)

// ErrD3825 is returned when ticket YAML input is malformed.
var ErrD3825 = errors.New("D3825: malformed ticket YAML")

// Row represents a single parsed ticket entry.
type Row struct {
	ID     string
	Phase  string
	Title  string
	Status string
	Deps   []string
}

// ParseTicketYAML parses a hand-rolled YAML list of ticket items into a slice of Row.
// Empty input returns a non-nil empty slice. Malformed input returns ErrD3825.
func ParseTicketYAML(src string) ([]Row, error) {
	if src == "" {
		return []Row{}, nil
	}

	lines := strings.Split(src, "\n")
	var rows []Row
	var current *Row

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "- id:") {
			if current != nil {
				rows = append(rows, *current)
			}
			val := strings.TrimSpace(strings.TrimPrefix(trimmed, "- id:"))
			current = &Row{ID: val}
			continue
		}

		if current == nil {
			return nil, ErrD3825
		}

		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			return nil, ErrD3825
		}

		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "phase":
			current.Phase = val
		case "title":
			current.Title = val
		case "status":
			current.Status = val
		case "deps":
			current.Deps = parseDeps(val)
		default:
			return nil, ErrD3825
		}
	}

	if current != nil {
		rows = append(rows, *current)
	}

	if rows == nil {
		return nil, ErrD3825
	}

	return rows, nil
}

// parseDeps extracts a string slice from a bracketed, comma-separated value like "[a, b]".
func parseDeps(s string) []string {
	s = strings.TrimSpace(s)
	if s == "[]" || s == "" {
		return nil
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		s = s[1 : len(s)-1]
	}
	parts := strings.Split(s, ",")
	var deps []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			deps = append(deps, p)
		}
	}
	return deps
}
