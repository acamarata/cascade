package crunch

import (
	"errors"
	"reflect"
	"testing"
)

func TestReshapeRequeueBlockedTicket(t *testing.T) {
	queue := []ReshapeTicket{
		{ID: "A", Blocked: true, Parents: []string{"P1"}},
		{ID: "B", Blocked: false},
	}

	got, err := ReshapeRequeue(queue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 tickets, got %d", len(got))
	}
	if got[0].ID != "B" {
		t.Errorf("expected first ticket to be B, got %s", got[0].ID)
	}
	if got[1].ID != "A" {
		t.Errorf("expected second ticket to be A, got %s", got[1].ID)
	}
	if !got[1].Blocked {
		t.Errorf("expected A to still be blocked")
	}

	// Input should remain unchanged.
	if queue[0].ID != "A" || queue[1].ID != "B" {
		t.Errorf("input was mutated")
	}
}

func TestAssumedParentReferenceAttached(t *testing.T) {
	queue := []ReshapeTicket{
		{ID: "X", Blocked: true, Parents: []string{"P", "P", "Q"}},
	}

	got, err := ReshapeRequeue(queue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(got))
	}
	want := []string{"P", "Q"}
	if !reflect.DeepEqual(got[0].AssumedParents, want) {
		t.Errorf("AssumedParents = %v, want %v", got[0].AssumedParents, want)
	}

	// Input should be unchanged.
	if !reflect.DeepEqual(queue[0].AssumedParents, []string(nil)) {
		t.Errorf("input AssumedParents mutated: %v", queue[0].AssumedParents)
	}
	if !reflect.DeepEqual(queue[0].Parents, []string{"P", "P", "Q"}) {
		t.Errorf("input Parents mutated: %v", queue[0].Parents)
	}

	// Missing parents on blocked ticket.
	_, err = ReshapeRequeue([]ReshapeTicket{{ID: "bad", Blocked: true}})
	if !errors.Is(err, ErrInvalidTicket) {
		t.Errorf("expected ErrInvalidTicket for blocked ticket without parents, got %v", err)
	}

	// Duplicate IDs.
	_, err = ReshapeRequeue([]ReshapeTicket{
		{ID: "dup", Blocked: true, Parents: []string{"P"}},
		{ID: "dup", Blocked: false},
	})
	if !errors.Is(err, ErrInvalidTicket) {
		t.Errorf("expected ErrInvalidTicket for duplicate IDs, got %v", err)
	}
}

func TestUnblockedTicketPassesThrough(t *testing.T) {
	queue := []ReshapeTicket{
		{ID: "U", Blocked: false, Parents: []string{"P1"}, AssumedParents: []string{"old"}},
	}

	got, err := ReshapeRequeue(queue)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(got))
	}
	if got[0].ID != "U" || got[0].Blocked || !reflect.DeepEqual(got[0].Parents, []string{"P1"}) || !reflect.DeepEqual(got[0].AssumedParents, []string{"old"}) {
		t.Errorf("unblocked ticket was altered: %+v", got[0])
	}
}

func TestEmptyQueueReturnsNoTickets(t *testing.T) {
	got, err := ReshapeRequeue(nil)
	if got != nil || err != nil {
		t.Errorf("nil input: got %v, %v", got, err)
	}

	got, err = ReshapeRequeue([]ReshapeTicket{})
	if got != nil || err != nil {
		t.Errorf("empty input: got %v, %v", got, err)
	}
}
