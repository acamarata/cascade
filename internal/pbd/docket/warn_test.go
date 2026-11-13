package docket

import "testing"

func countWarnings(warnings []Warning, code string) int {
	n := 0
	for _, w := range warnings {
		if w.Code == code {
			n++
		}
	}
	return n
}

func TestWarnMissingContext(t *testing.T) {
	in := &WarnInput{
		Ticket:    TicketInfo{Context: ""},
		Epic:      EpicInfo{CriticalReviewPlan: true},
		WaveWidth: 8,
	}

	warnings, err := WarnMissing(in)
	if err != nil {
		t.Fatalf("WarnMissing returned error: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(warnings), warnings)
	}
	if got := countWarnings(warnings, "D4633"); got != 1 {
		t.Fatalf("expected one D4633 warning, got %d", got)
	}
	if warnings[0].Subject == "" || warnings[0].Message == "" {
		t.Fatalf("expected subject and message to be set, got %+v", warnings[0])
	}
}

func TestWarnMissingReviewPlan(t *testing.T) {
	in := &WarnInput{
		Ticket:    TicketInfo{Context: "ticket context"},
		Epic:      EpicInfo{CriticalReviewPlan: false},
		WaveWidth: 8,
	}

	warnings, err := WarnMissing(in)
	if err != nil {
		t.Fatalf("WarnMissing returned error: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(warnings), warnings)
	}
	if got := countWarnings(warnings, "D4633"); got != 1 {
		t.Fatalf("expected one D4633 warning, got %d", got)
	}
	if warnings[0].Subject == "" || warnings[0].Message == "" {
		t.Fatalf("expected subject and message to be set, got %+v", warnings[0])
	}
}

func TestWarnNarrowWave(t *testing.T) {
	in := &WarnInput{
		Ticket:    TicketInfo{Context: "ticket context"},
		Epic:      EpicInfo{CriticalReviewPlan: true},
		WaveWidth: 7,
	}

	warnings, err := WarnMissing(in)
	if err != nil {
		t.Fatalf("WarnMissing returned error: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %+v", len(warnings), warnings)
	}
	if got := countWarnings(warnings, "D4633"); got != 1 {
		t.Fatalf("expected one D4633 warning, got %d", got)
	}
	if warnings[0].Subject == "" || warnings[0].Message == "" {
		t.Fatalf("expected subject and message to be set, got %+v", warnings[0])
	}
}

func TestWarnNoIssues(t *testing.T) {
	in := &WarnInput{
		Ticket:    TicketInfo{Context: "ticket context"},
		Epic:      EpicInfo{CriticalReviewPlan: true},
		WaveWidth: 8,
	}

	warnings, err := WarnMissing(in)
	if err != nil {
		t.Fatalf("WarnMissing returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %+v", warnings)
	}
}

func TestWarnInvalidInput(t *testing.T) {
	warnings, err := WarnMissing(nil)
	if err == nil {
		t.Fatal("expected error for nil input, got nil")
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings for nil input, got %+v", warnings)
	}
}
