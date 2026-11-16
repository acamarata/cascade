package forge

import (
	"errors"
	"testing"
)

// TestRouteTicketParksUnlandedDependency ensures that a ticket depending on an
// unlanded owner-chain ticket is parked with zero execution.
func TestRouteTicketParksUnlandedDependency(t *testing.T) {
	unlanded := Ticket{OwnerChainLanded: false}
	ticket := Ticket{
		IsAcceptance:     true,
		ReadsSharedFile:  true,
		Dependencies:     []Ticket{unlanded},
		OwnerChainLanded: true,
	}

	id, err := RouteTicket(ticket)
	if !errors.Is(err, ErrParked) {
		t.Fatalf("expected ErrParked, got %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty author ID for parked ticket, got %q", id)
	}
}

// TestRouteTicketRoutesNativeAuthor ensures that acceptance tickets reading
// shared files route to native authors.
func TestRouteTicketRoutesNativeAuthor(t *testing.T) {
	ticket := Ticket{
		IsAcceptance:     true,
		ReadsSharedFile:  true,
		OwnerChainLanded: true,
	}

	id, err := RouteTicket(ticket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != NativeAuthorID {
		t.Fatalf("expected NativeAuthorID, got %q", id)
	}
}

// TestRouteTicketRoutesNormally ensures that valid independent tickets route normally.
func TestRouteTicketRoutesNormally(t *testing.T) {
	ticket := Ticket{
		IsAcceptance:     false,
		ReadsSharedFile:  false,
		OwnerChainLanded: true,
	}

	id, err := RouteTicket(ticket)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == NativeAuthorID {
		t.Fatalf("expected normal routing, got native author")
	}
}
