package pews

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAuthorParseCanonicalID(t *testing.T) {
	valid := []string{
		"P1-E14-W3-S28-T3", "P1-E01-W1-S01-T1", "P2-E27-W10-S99-T123",
	}
	for _, id := range valid {
		c, err := parseCanonicalID(id)
		if err != nil {
			t.Errorf("parseCanonicalID(%q) unexpected error: %v", id, err)
			continue
		}
		if got := canonicalID(c.phase, c.epicNum, c.wave, c.sprint, c.ticket); got != id {
			t.Errorf("round trip: canonicalID(parseCanonicalID(%q)) = %q", id, got)
		}
	}

	invalid := []string{
		"", "not-an-id", "P1-E14-W3-S28",
		"P1-E5-W3-S28-T3",   // epic under-padded
		"P1-E14-W3-S8-T3",   // sprint under-padded
		"P1-EN-W3-S28-T3",   // epic must be numeric here, not letters
		"P1-E14-W03-S28-T3", // wave must never be padded
	}
	for _, id := range invalid {
		if _, err := parseCanonicalID(id); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("parseCanonicalID(%q) err = %v, want KindInvalidInput", id, err)
		}
	}
}

func TestAuthorLettersFromEpicNumber(t *testing.T) {
	for _, n := range []int{1, 14, 26, 27, 34, 44, 52, 676, 677, 702, 703} {
		letters := lettersFromEpicNumber(n)
		if got := epicNumberFromLetters(letters); got != n {
			t.Errorf("lettersFromEpicNumber(%d) = %q, epicNumberFromLetters back = %d, want %d", n, letters, got, n)
		}
	}
	cases := map[int]string{1: "A", 14: "N", 26: "Z", 27: "AA", 34: "AH", 44: "AR"}
	for n, want := range cases {
		if got := lettersFromEpicNumber(n); got != want {
			t.Errorf("lettersFromEpicNumber(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestAuthorRelPathFor(t *testing.T) {
	c := idComponents{phase: "P1", epicNum: 14, wave: 3, sprint: 28, ticket: 3}
	want := "epics/E-N/waves/W-3/sprints/S-28/tickets/T-3.yaml"
	if got := relPathFor(c); got != want {
		t.Errorf("relPathFor = %q, want %q", got, want)
	}
}

func TestAuthorRecordFor(t *testing.T) {
	ticket := cleanTicket(t, "P1-E14-W3-S28-T3")
	c, err := parseCanonicalID(ticket.ID)
	if err != nil {
		t.Fatalf("parseCanonicalID: %v", err)
	}
	rel := relPathFor(c)
	rec := recordFor(ticket, c, rel)
	if rec.ID != ticket.ID || rec.CanonicalID != ticket.ID || rec.RelPath != rel ||
		rec.EpicLetters != "N" || rec.EpicNum != 14 || rec.Wave != 3 || rec.Sprint != 28 || rec.TicketNum != 3 {
		t.Errorf("recordFor = %+v", rec)
	}
}

func TestAuthorRequirePhase(t *testing.T) {
	c := idComponents{phase: "P1", epicNum: 14, wave: 3, sprint: 28, ticket: 3}
	if err := requirePhase(c, "P1", "P1-E14-W3-S28-T3"); err != nil {
		t.Errorf("matching phase: %v", err)
	}
	if err := requirePhase(c, "P2", "P1-E14-W3-S28-T3"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("mismatched phase err = %v, want KindInvalidInput", err)
	}
}

// cleanTicket decodes cleanTicketYAML(id) (lint_test.go) into a Ticket so
// author tests can mutate one field at a time from a known-lint-clean
// starting point.
func cleanTicket(t *testing.T, id string) Ticket {
	t.Helper()
	tk, err := DecodeTicket([]byte(cleanTicketYAML(id)))
	if err != nil {
		t.Fatalf("DecodeTicket(cleanTicketYAML(%q)): %v", id, err)
	}
	return tk
}
