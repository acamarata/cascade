package crunch

import (
	"testing"
)

// TestPurifyAppliesRedactThenJev proves that PurifyCW calls the redact
// stage first and the jev stage second, forwards the exact outputs between
// stages, and returns the exact final jev output without trimming it.
func TestPurifyAppliesRedactThenJev(t *testing.T) {
	transcript := "  original transcript \n"
	redactOutput := "\tredacted transcript\t\n"
	jevOutput := "  jev final \n "

	var calls []string
	var inputs []string
	redactCalls := 0
	jevCalls := 0

	redact := Stage(func(s string) (string, error) {
		redactCalls++
		calls = append(calls, "redact")
		inputs = append(inputs, s)
		return redactOutput, nil
	})

	jev := Stage(func(s string) (string, error) {
		jevCalls++
		calls = append(calls, "jev")
		inputs = append(inputs, s)
		return jevOutput, nil
	})

	got, err := PurifyCW(transcript, redact, jev)
	if err != nil {
		t.Fatalf("PurifyCW returned unexpected error: %v", err)
	}

	if got != jevOutput {
		t.Fatalf("PurifyCW returned %q, want exact jev output %q", got, jevOutput)
	}

	if redactCalls != 1 {
		t.Fatalf("redact stage called %d times, want exactly 1", redactCalls)
	}

	if jevCalls != 1 {
		t.Fatalf("jev stage called %d times, want exactly 1", jevCalls)
	}

	if len(calls) != 2 {
		t.Fatalf("got %d stage calls, want 2", len(calls))
	}

	if calls[0] != "redact" || calls[1] != "jev" {
		t.Fatalf("stage order is %v, want [redact jev]", calls)
	}

	if len(inputs) != 2 {
		t.Fatalf("recorded %d stage inputs, want 2", len(inputs))
	}

	if inputs[0] != transcript {
		t.Fatalf("redact received %q, want original transcript %q", inputs[0], transcript)
	}

	if inputs[1] != redactOutput {
		t.Fatalf("jev received %q, want exact redact output %q", inputs[1], redactOutput)
	}
}
