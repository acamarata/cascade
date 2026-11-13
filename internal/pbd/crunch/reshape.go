// Package crunch provides queue reshaping utilities.
package crunch

import "errors"

// ReshapeTicket represents a task queue item that can be reshaped.
type ReshapeTicket struct {
	ID             string
	Blocked        bool
	Parents        []string
	AssumedParents []string
}

// ErrInvalidTicket indicates that a ticket or its references are invalid.
var ErrInvalidTicket = errors.New("crunch: invalid ticket")

// validateReshapeQueue rejects empty or duplicate IDs, empty parents and
// blocked tickets without parents.
func validateReshapeQueue(queue []ReshapeTicket) error {
	seen := make(map[string]bool)
	for _, t := range queue {
		if t.ID == "" {
			return ErrInvalidTicket
		}
		if seen[t.ID] {
			return ErrInvalidTicket
		}
		seen[t.ID] = true
		for _, p := range t.Parents {
			if p == "" {
				return ErrInvalidTicket
			}
		}
		if t.Blocked && len(t.Parents) == 0 {
			return ErrInvalidTicket
		}
	}
	return nil
}

// ReshapeRequeue reorders tickets and populates assumed parents for blocked tickets.
func ReshapeRequeue(queue []ReshapeTicket) ([]ReshapeTicket, error) {
	if len(queue) == 0 {
		return nil, nil
	}

	if err := validateReshapeQueue(queue); err != nil {
		return nil, err
	}

	var unblocked []ReshapeTicket
	var blocked []ReshapeTicket

	for _, t := range queue {
		if t.Blocked {
			blocked = append(blocked, t)
		} else {
			unblocked = append(unblocked, t)
		}
	}

	for i, t := range blocked {
		seenP := make(map[string]bool)
		var deduped []string
		for _, p := range t.Parents {
			if !seenP[p] {
				seenP[p] = true
				deduped = append(deduped, p)
			}
		}

		newT := t
		newT.Parents = make([]string, len(t.Parents))
		copy(newT.Parents, t.Parents)

		newT.AssumedParents = make([]string, len(deduped))
		copy(newT.AssumedParents, deduped)

		blocked[i] = newT
	}

	result := make([]ReshapeTicket, 0, len(unblocked)+len(blocked))
	result = append(result, unblocked...)
	result = append(result, blocked...)

	return result, nil
}
