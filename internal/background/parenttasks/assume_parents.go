// Package parenttasks implements parent task assumptions for tickets.
package parenttasks

import "errors"

// Ticket represents a task or ticket with parents.
type Ticket struct {
	ID             string
	Parents        []string
	AssumedParents []string
}

// ErrInvalidParentInput is returned when the input ticket has invalid parent data.
var ErrInvalidParentInput = errors.New("invalid parent input")

// AssumeParents validates the input ticket and returns it with assumptions applied.
// It validates entire input before completeness checks.
// Empty ID, empty parent ID, duplicate Parents or duplicate/empty AssumedParents entries return Ticket{}, ErrInvalidParentInput.
// Existing nonempty AssumedParents must be an ordered subsequence of Parents.
// When Parents is empty it is preserved unchanged (still reject duplicate/empty entries in AssumedParents).
// With nil/empty Parents return the ticket unchanged, nil error.
// Never mutates the input ticket, slices or done map.
func AssumeParents(ticket Ticket, done map[string]bool) (Ticket, error) {
	// Validate empty ID
	if ticket.ID == "" {
		return Ticket{}, ErrInvalidParentInput
	}

	// Validate Parents: empty parent ID or duplicates
	parentSet := make(map[string]bool, len(ticket.Parents))
	for _, p := range ticket.Parents {
		if p == "" {
			return Ticket{}, ErrInvalidParentInput
		}
		if parentSet[p] {
			return Ticket{}, ErrInvalidParentInput
		}
		parentSet[p] = true
	}

	// Validate AssumedParents: empty entries or duplicates
	assumedSet := make(map[string]bool, len(ticket.AssumedParents))
	for _, ap := range ticket.AssumedParents {
		if ap == "" {
			return Ticket{}, ErrInvalidParentInput
		}
		if assumedSet[ap] {
			return Ticket{}, ErrInvalidParentInput
		}
		assumedSet[ap] = true
	}

	// Check if AssumedParents is an ordered subsequence of Parents,
	// UNLESS Parents is empty.
	if len(ticket.Parents) > 0 && len(ticket.AssumedParents) > 0 {
		pIdx := 0
		for _, ap := range ticket.AssumedParents {
			found := false
			for pIdx < len(ticket.Parents) {
				if ticket.Parents[pIdx] == ap {
					found = true
					pIdx++
					break
				}
				pIdx++
			}
			if !found {
				return Ticket{}, ErrInvalidParentInput
			}
		}
	}

	// Return ticket unchanged for nil/empty parents, and for now for valid non-empty parents
	// as completeness checks are out of scope for this ticket.
	return ticket, nil
}
