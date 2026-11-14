// Package frontier provides ticket ranking based on dependency chain depth.
package frontier

import (
	"errors"
	"sort"
)

// Ticket represents a unit of work with an identifier, protection flag,
// and a list of dependent ticket identifiers that it unblocks.
type Ticket struct {
	// ID is the unique identifier for this ticket.
	ID string
	// Protected indicates whether this ticket should be excluded from the frontier.
	Protected bool
	// Dependents lists the IDs of tickets that depend on this one.
	Dependents []string
}

// Frontier computes the set of ready tickets sorted by longest dependency
// path descending, then by number of dependents descending.
type Frontier struct{}

// Ready filters out protected tickets, computes the longest remaining path
// for each candidate using LongestPath from chain.go, and returns them
// sorted descending by path length. Ties are broken by descending count
// of dependents (len(Dependents)). Tickets involved in cycles are skipped.
func (f *Frontier) Ready(candidates []Ticket, edges map[string][]string) ([]Ticket, error) {
	memo := NewChainMemo()

	type scored struct {
		ticket Ticket
		path   int
	}

	var result []scored

	for _, c := range candidates {
		if c.Protected {
			continue
		}

		pathLen, err := LongestPath(c.ID, edges, memo)
		if err != nil {
			if errors.Is(err, ErrCycleDetected) {
				continue
			}
			return nil, err
		}

		result = append(result, scored{ticket: c, path: pathLen})
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].path != result[j].path {
			return result[i].path > result[j].path
		}
		return len(result[i].ticket.Dependents) > len(result[j].ticket.Dependents)
	})

	out := make([]Ticket, len(result))
	for i, s := range result {
		out[i] = s.ticket
	}

	return out, nil
}
