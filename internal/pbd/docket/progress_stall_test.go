package docket

import (
	"reflect"
	"testing"
)

// TestNewProgressStallIncidentFromTicket verifies a ticket subject creates a
// progress_stall incident without error.
func TestNewProgressStallIncidentFromTicket(t *testing.T) {
	got, err := NewProgressStallIncident("ticket:DKT:032")
	if err != nil {
		t.Fatalf("NewProgressStallIncident returned error: %v", err)
	}
	if got.Kind != "progress_stall" {
		t.Fatalf("Kind = %q, want %q", got.Kind, "progress_stall")
	}
	if got.Subject != "ticket:DKT:032" {
		t.Fatalf("Subject = %q, want %q", got.Subject, "ticket:DKT:032")
	}
}

// TestNewProgressStallIncidentFromFrontier verifies a frontier subject creates
// a progress_stall incident without error.
func TestNewProgressStallIncidentFromFrontier(t *testing.T) {
	got, err := NewProgressStallIncident("frontier:DKT")
	if err != nil {
		t.Fatalf("NewProgressStallIncident returned error: %v", err)
	}
	if got.Kind != "progress_stall" {
		t.Fatalf("Kind = %q, want %q", got.Kind, "progress_stall")
	}
	if got.Subject != "frontier:DKT" {
		t.Fatalf("Subject = %q, want %q", got.Subject, "frontier:DKT")
	}
}

// TestNewProgressStallIncidentRejectsEmptySubject verifies that an empty
// subject fails closed with the required error and a zero incident.
func TestNewProgressStallIncidentRejectsEmptySubject(t *testing.T) {
	got, err := NewProgressStallIncident("")
	if err == nil {
		t.Fatal("NewProgressStallIncident returned nil error for empty subject")
	}
	if err.Error() != "progress_stall: empty subject" {
		t.Fatalf("error = %q, want %q", err.Error(), "progress_stall: empty subject")
	}
	if got.Kind != "" || got.Subject != "" || len(got.Refs) != 0 {
		t.Fatalf("Incident = %+v, want zero Incident", got)
	}
}

// TestProgressStallIncidentPreservesReferences verifies the incident preserves
// the required extension references.
func TestProgressStallIncidentPreservesReferences(t *testing.T) {
	got, err := NewProgressStallIncident("ticket:DKT:032")
	if err != nil {
		t.Fatalf("NewProgressStallIncident returned error: %v", err)
	}
	want := []string{"P2-NSC-09", "P2-DEV-08", "D4926"}
	if !reflect.DeepEqual(got.Refs, want) {
		t.Fatalf("Refs = %v, want %v", got.Refs, want)
	}
}
