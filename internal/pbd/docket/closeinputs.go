// Package docket provides ticket input management.
package docket

import "errors"

// Input represents a ticket input with its current status.
type Input struct {
	TicketID string
	Status   string
}

// CloseInputsStore holds the in-memory collection of inputs.
type CloseInputsStore struct {
	Inputs []Input
}

const (
	// InputOpen indicates an input that has not been taken or closed.
	InputOpen = "open"
	// InputTaken indicates an input that has been claimed.
	InputTaken = "taken"
	// InputClosed indicates an input that is no longer available.
	InputClosed = "closed"
)

// ErrTicketUnknown is returned when a ticket ID does not match any input.
var ErrTicketUnknown = errors.New("ERR_TICKET_UNKNOWN")

// CloseTicket sets every untaken input of the specified ticket to closed
// when the ticket ends. Inputs already taken remain unchanged. Inputs
// belonging to other tickets are not affected. Returns ErrTicketUnknown
// if no input in the store matches the given ticketID; in that case no
// changes are made.
func CloseTicket(s *CloseInputsStore, ticketID string) error {
	found := false
	for i := range s.Inputs {
		if s.Inputs[i].TicketID == ticketID {
			found = true
			if s.Inputs[i].Status == InputOpen {
				s.Inputs[i].Status = InputClosed
			}
		}
	}
	if !found {
		return ErrTicketUnknown
	}
	return nil
}
