// Package docket provides ticket event retention tests.
package docket

import (
	"errors"
	"testing"
	"time"
)

func TestTrimRetains90Days(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	cutoff := 2160 * time.Hour

	e1 := RetentionEvent{TicketID: "T1", At: now, Kind: "K1"}
	e2 := RetentionEvent{TicketID: "T2", At: now.Add(-cutoff), Kind: "K2"}

	events := []RetentionEvent{e1, e2}

	got, err := TrimEvents(events, now, cutoff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0] != e1 || got[1] != e2 {
		t.Errorf("events not retained in order")
	}

	// cutoff 0 keeps only the event at now
	e3 := RetentionEvent{TicketID: "T3", At: now.Add(-time.Hour), Kind: "K3"}
	events2 := []RetentionEvent{e1, e3}
	got2, err := TrimEvents(events2, now, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("expected 1 event for cutoff 0, got %d", len(got2))
	}
	if got2[0] != e1 {
		t.Errorf("expected e1 for cutoff 0")
	}
}

func TestTrimDropsOlderEvents(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	e := RetentionEvent{TicketID: "T1", At: now.Add(-91 * 24 * time.Hour), Kind: "K1"}
	events := []RetentionEvent{e}

	got, err := TrimEvents(events, now, 2160*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil slice, got %v", got)
	}
}

func TestTrimNegativeCutoffFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

	checkErr := func(err error, msg string) {
		if err == nil {
			t.Errorf("%s: expected error, got nil", msg)
			return
		}
		if !errors.Is(err, ErrEventRetention) {
			t.Errorf("%s: expected ErrEventRetention, got %v", msg, err)
		}
		if err.Error() != "D4597" {
			t.Errorf("%s: expected error text D4597, got %s", msg, err.Error())
		}
	}

	// negative duration
	_, err := TrimEvents(nil, now, -time.Hour)
	checkErr(err, "negative duration")

	// zero now
	_, err = TrimEvents(nil, time.Time{}, time.Hour)
	checkErr(err, "zero now")

	// empty kind or ID
	_, err = TrimEvents([]RetentionEvent{{TicketID: "T1", At: now, Kind: ""}}, now, time.Hour)
	checkErr(err, "empty kind")

	_, err = TrimEvents([]RetentionEvent{{TicketID: "", At: now, Kind: "K1"}}, now, time.Hour)
	checkErr(err, "empty ticketID")

	// zero event time
	_, err = TrimEvents([]RetentionEvent{{TicketID: "T1", At: time.Time{}, Kind: "K1"}}, now, time.Hour)
	checkErr(err, "zero event time")

	// future event time
	_, err = TrimEvents([]RetentionEvent{{TicketID: "T1", At: now.Add(time.Hour), Kind: "K1"}}, now, time.Hour)
	checkErr(err, "future event time")

	// duplicate tuple
	_, err = TrimEvents([]RetentionEvent{
		{TicketID: "T1", At: now, Kind: "K1"},
		{TicketID: "T1", At: now, Kind: "K1"},
	}, now, time.Hour)
	checkErr(err, "duplicate tuple")

	// expired malformed rows fail
	_, err = TrimEvents([]RetentionEvent{
		{TicketID: "T1", At: now.Add(-100 * 24 * time.Hour), Kind: ""},
	}, now, 90*24*time.Hour)
	checkErr(err, "expired malformed rows")
}
