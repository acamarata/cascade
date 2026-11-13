package docket

import (
	"errors"
	"strings"
	"testing"
)

// TestParseTicketVerifyExactlyOne ensures a ticket with exactly one verify
// command is accepted.
func TestParseTicketVerifyExactlyOne(t *testing.T) {
	lines := []string{"title: t", "verify: run"}

	if err := ParseTicket(lines); err != nil {
		t.Fatalf("ParseTicket(%q) = %v, want nil", lines, err)
	}
}

// TestParseTicketVerifyZero ensures a ticket without a verify command is
// rejected with error D2225.
func TestParseTicketVerifyZero(t *testing.T) {
	lines := []string{"title: t"}

	err := ParseTicket(lines)
	if err == nil {
		t.Fatalf("ParseTicket(%q) = nil, want D2225", lines)
	}
	if !errors.Is(err, ErrD2225) {
		t.Fatalf("ParseTicket(%q) = %v, want ErrD2225", lines, err)
	}
	if !strings.HasPrefix(err.Error(), "D2225: ") {
		t.Fatalf("error = %q, want prefix \"D2225: \"", err.Error())
	}
}

// TestParseTicketVerifyMultiple ensures a ticket with multiple verify commands
// is rejected with error D2225.
func TestParseTicketVerifyMultiple(t *testing.T) {
	lines := []string{"verify: a", "verify: b"}

	err := ParseTicket(lines)
	if err == nil {
		t.Fatalf("ParseTicket(%q) = nil, want D2225", lines)
	}
	if !errors.Is(err, ErrD2225) {
		t.Fatalf("ParseTicket(%q) = %v, want ErrD2225", lines, err)
	}
	if !strings.HasPrefix(err.Error(), "D2225: ") {
		t.Fatalf("error = %q, want prefix \"D2225: \"", err.Error())
	}
}

// TestParseTicketVerifyEmpty ensures an empty ticket is rejected with error
// D2225.
func TestParseTicketVerifyEmpty(t *testing.T) {
	var lines []string

	err := ParseTicket(lines)
	if err == nil {
		t.Fatalf("ParseTicket(%q) = nil, want D2225", lines)
	}
	if !errors.Is(err, ErrD2225) {
		t.Fatalf("ParseTicket(%q) = %v, want ErrD2225", lines, err)
	}
	if !strings.HasPrefix(err.Error(), "D2225: ") {
		t.Fatalf("error = %q, want prefix \"D2225: \"", err.Error())
	}
}
