// Package docket manages QG gap drafting and token folding.
package docket

import "fmt"

// Gap represents a gap with an ID, origin, QG ticket, and acceptance tokens.
type Gap struct {
	ID     string
	Origin string
	QG     string
	Tokens []string
}

// Report holds the result of processing gaps.
type Report struct {
	Pending []string
	Folded  map[string][]string
	Drafts  []string
}

// Mark processes gaps according to landed QG tickets.
// Gaps whose QG ticket has not landed are added to Pending without re-drafting.
// Repeated acceptance tokens on a QG ticket are folded into its origin gap per D5106 and D5107.
// Unknown tokens return an error prefixed with D5106.
func Mark(gaps []Gap, landed map[string]bool) (Report, error) {
	r := Report{
		Folded: make(map[string][]string),
	}
	for _, g := range gaps {
		if !landed[g.QG] {
			r.Pending = append(r.Pending, g.ID)
			continue
		}
		seen := make(map[string]bool)
		for _, t := range g.Tokens {
			switch t {
			case "ACCEPT", "DEFER", "DONE":
				if !seen[t] {
					seen[t] = true
					r.Folded[g.Origin] = append(r.Folded[g.Origin], t)
				}
			default:
				return r, fmt.Errorf("D5106: unknown token %s", t)
			}
		}
	}
	return r, nil
}
