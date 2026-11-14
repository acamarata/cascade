// Package docket provides ticket filtering for the pbd system.
package docket

// TicketsOpenTicket represents a ticket used by the open filter.
type TicketsOpenTicket struct {
	// ID is the unique identifier of the ticket.
	ID string
	// Phase is the phase the ticket belongs to.
	Phase string
	// Status is the current status of the ticket.
	Status string
}

// TicketFilter holds parameters for filtering tickets.
type TicketFilter struct {
	// Open enables filtering out done and deferred tickets when true.
	Open bool
	// Phase filters tickets to a specific phase. When empty and Open is true, BuildingPhase is used.
	Phase string
	// BuildingPhase is the default phase used when Open is true and Phase is empty.
	BuildingPhase string
}

// FilterTickets returns a filtered copy of the provided tickets based on the given filter.
// When f.Open is true, tickets with status "done" or "deferred" are excluded.
// If f.Open is true and f.Phase is empty, f.BuildingPhase is used as the phase filter.
// An explicit f.Phase overrides the building phase default.
// When f.Open is false, the original slice is returned unchanged.
func FilterTickets(tickets []TicketsOpenTicket, f TicketFilter) []TicketsOpenTicket {
	if !f.Open {
		return tickets
	}

	phase := f.Phase
	if phase == "" {
		phase = f.BuildingPhase
	}

	var result []TicketsOpenTicket
	for _, t := range tickets {
		if t.Status == "done" || t.Status == "deferred" {
			continue
		}
		if phase != "" && t.Phase != phase {
			continue
		}
		result = append(result, t)
	}
	return result
}
