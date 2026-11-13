package docket

import (
	"reflect"
	"testing"
)

func TestOpenFilterExcludesDoneAndDeferred(t *testing.T) {
	tickets := []TicketsOpenTicket{
		{ID: "1", Phase: "P1", Status: "done"},
		{ID: "2", Phase: "P1", Status: "deferred"},
		{ID: "3", Phase: "P1", Status: "building"},
	}
	f := TicketFilter{Open: true}
	got := FilterTickets(tickets, f)
	want := []TicketsOpenTicket{{ID: "3", Phase: "P1", Status: "building"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpenFilterKeepsActiveStates(t *testing.T) {
	tickets := []TicketsOpenTicket{
		{ID: "1", Phase: "P1", Status: "todo"},
		{ID: "2", Phase: "P1", Status: "building"},
		{ID: "3", Phase: "P1", Status: "blocked"},
	}
	f := TicketFilter{Open: true}
	got := FilterTickets(tickets, f)
	if len(got) != 3 {
		t.Errorf("got %d tickets, want 3", len(got))
	}
}

func TestOpenFilterDefaultsToBuildingPhase(t *testing.T) {
	tickets := []TicketsOpenTicket{
		{ID: "1", Phase: "P7", Status: "building"},
		{ID: "2", Phase: "P8", Status: "building"},
	}
	f := TicketFilter{Open: true, BuildingPhase: "P8"}
	got := FilterTickets(tickets, f)
	want := []TicketsOpenTicket{{ID: "2", Phase: "P8", Status: "building"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestOpenFilterExplicitPhaseWins(t *testing.T) {
	tickets := []TicketsOpenTicket{
		{ID: "1", Phase: "P7", Status: "building"},
		{ID: "2", Phase: "P8", Status: "building"},
	}
	f := TicketFilter{Open: true, Phase: "P7", BuildingPhase: "P8"}
	got := FilterTickets(tickets, f)
	want := []TicketsOpenTicket{{ID: "1", Phase: "P7", Status: "building"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
