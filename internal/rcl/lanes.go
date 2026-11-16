// Package rcl implements ranking cascades for retrieval.
package rcl

import (
	"path/filepath"
	"strings"
	"time"
)

// ExactWeight is the lane weight assigned to exact matches of ticket IDs,
// file names, or phase IDs.
const ExactWeight float64 = 2.0

// LaneItem is a candidate item evaluated by lane filters.
type LaneItem struct {
	TicketID string
	FileName string
	PhaseID  string
	Changed  time.Time
}

// ExactLane returns items whose ticket ID, file name (with or without
// extension) or phase ID appears as a whole whitespace-delimited token in
// the query, after stripping boundary punctuation. Matching is
// case-sensitive. Empty query returns nothing; empty fields never match.
// The returned slice is nonnil and preserves input order and duplicates.
func ExactLane(query string, items []LaneItem) []LaneItem {
	if len(items) == 0 {
		return []LaneItem{}
	}
	if query == "" {
		return []LaneItem{}
	}

	tokens := strings.Fields(query)
	cleaned := make(map[string]bool, len(tokens))
	for _, t := range tokens {
		t = strings.Trim(t, ",;:()[]{}")
		if t != "" {
			cleaned[t] = true
		}
	}
	if len(cleaned) == 0 {
		return []LaneItem{}
	}

	var out []LaneItem
	for _, item := range items {
		if item.TicketID != "" && cleaned[item.TicketID] {
			out = append(out, item)
			continue
		}
		if item.PhaseID != "" && cleaned[item.PhaseID] {
			out = append(out, item)
			continue
		}
		if item.FileName != "" {
			base := filepath.Base(item.FileName)
			if base != "" && cleaned[base] {
				out = append(out, item)
				continue
			}
			ext := filepath.Ext(base)
			if ext != "" {
				stripped := base[:len(base)-len(ext)]
				if stripped != "" && cleaned[stripped] {
					out = append(out, item)
					continue
				}
			}
		}
	}
	if out == nil {
		return []LaneItem{}
	}
	return out
}
