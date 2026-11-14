package crunch

import (
	"testing"
)

// TestPurifyAppliesRedactThenSys1B proves that PurifyWebchat calls the redact
// stage first and the sys1b stage second, forwards the exact outputs between
// stages, and returns the exact final sys1b output without trimming it.
func TestPurifyAppliesRedactThenSys1B(t *testing.T) {
	transcript := "  original transcript \n"
	redactOutput := "\tredacted transcript\t\n"
	sys1bOutput := "  sys1b final \n "

	rec := &stageRecorder{}
	redact := rec.stage("redact", redactOutput)
	sys1b := rec.stage("sys1b", sys1bOutput)

	got, err := PurifyWebchat(transcript, redact, sys1b)
	if err != nil {
		t.Fatalf("PurifyWebchat returned unexpected error: %v", err)
	}

	if got != sys1bOutput {
		t.Fatalf("PurifyWebchat returned %q, want exact sys1b output %q", got, sys1bOutput)
	}

	rec.check(t, transcript, redactOutput)
}

// stageRecorder records the order and inputs of stage calls.
type stageRecorder struct {
	calls  []string
	inputs []string
}

func (r *stageRecorder) stage(name, output string) Stage {
	return func(s string) (string, error) {
		r.calls = append(r.calls, name)
		r.inputs = append(r.inputs, s)
		return output, nil
	}
}

func (r *stageRecorder) count(name string) int {
	n := 0
	for _, c := range r.calls {
		if c == name {
			n++
		}
	}
	return n
}

func (r *stageRecorder) check(t *testing.T, transcript, redactOutput string) {
	t.Helper()
	if n := r.count("redact"); n != 1 {
		t.Fatalf("redact stage called %d times, want exactly 1", n)
	}
	if n := r.count("sys1b"); n != 1 {
		t.Fatalf("sys1b stage called %d times, want exactly 1", n)
	}
	if len(r.calls) != 2 {
		t.Fatalf("got %d stage calls, want 2", len(r.calls))
	}
	if r.calls[0] != "redact" || r.calls[1] != "sys1b" {
		t.Fatalf("stage order is %v, want [redact sys1b]", r.calls)
	}
	if len(r.inputs) != 2 {
		t.Fatalf("recorded %d stage inputs, want 2", len(r.inputs))
	}
	if r.inputs[0] != transcript {
		t.Fatalf("redact received %q, want original transcript %q", r.inputs[0], transcript)
	}
	if r.inputs[1] != redactOutput {
		t.Fatalf("sys1b received %q, want exact redact output %q", r.inputs[1], redactOutput)
	}
}
