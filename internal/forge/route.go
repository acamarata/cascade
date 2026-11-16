// Package forge provides routing and dependency parking for acceptance tickets.
package forge

import "errors"

// ErrParked is returned when a ticket depends on an unlanded owner-chain ticket.
var ErrParked = errors.New("ticket parked due to unlanded dependency")

// NativeAuthorID is the default author identifier for native routing.
const NativeAuthorID AuthorID = "native-author"

// AuthorID represents the unique identifier of a ticket author.
type AuthorID string

// Ticket represents a work item that may require routing or parking.
type Ticket struct {
	// IsAcceptance indicates whether this is an acceptance/SPORT ticket.
	IsAcceptance bool
	// ReadsSharedFile indicates whether the ticket reads a large shared file.
	ReadsSharedFile bool
	// Dependencies lists tickets this ticket depends on.
	Dependencies []Ticket
	// OwnerChainLanded indicates whether the owner chain has landed.
	OwnerChainLanded bool
}

// RouteTicket determines the author for a ticket or parks it if dependencies are unlanded.
// Acceptance tickets reading shared files are routed to the native author by default.
// If any dependency has not landed its owner chain, ErrParked is returned.
func RouteTicket(t Ticket) (AuthorID, error) {
	for _, dep := range t.Dependencies {
		if !dep.OwnerChainLanded {
			return "", ErrParked
		}
	}

	if t.ReadsSharedFile && t.IsAcceptance {
		return NativeAuthorID, nil
	}

	return "default-author", nil
}
