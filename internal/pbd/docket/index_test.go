package docket

import "testing"

func TestAddAndLookupByTicketKind(t *testing.T) {
	idx := NewIndex()
	idx.Add(Event{Ticket: "T1", Kind: "created", Time: 1})
	events := idx.Lookup("T1", "created")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}

func TestLookupReturnsSortedByTime(t *testing.T) {
	idx := NewIndex()
	idx.Add(Event{Ticket: "T1", Kind: "created", Time: 2})
	idx.Add(Event{Ticket: "T1", Kind: "created", Time: 1})
	events := idx.Lookup("T1", "created")
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Time != 1 || events[1].Time != 2 {
		t.Fatalf("expected order 1 then 2, got %v then %v", events[0].Time, events[1].Time)
	}
}

func TestLookupEmptyIndex(t *testing.T) {
	idx := NewIndex()
	events := idx.Lookup("T1", "created")
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestLookupNoMatch(t *testing.T) {
	idx := NewIndex()
	idx.Add(Event{Ticket: "T1", Kind: "created", Time: 1})
	events := idx.Lookup("T2", "created")
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestAddDuplicateEvent(t *testing.T) {
	idx := NewIndex()
	idx.Add(Event{Ticket: "T1", Kind: "created", Time: 1})
	idx.Add(Event{Ticket: "T1", Kind: "created", Time: 1})
	events := idx.Lookup("T1", "created")
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
}
