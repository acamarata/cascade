// Purpose: tests for `cascade memory`'s rendering and redaction half
//
//	(memory_view.go): the canary proving no machine path or
//	secret-shaped value leaves this command in a diagnostic, the guard
//	against the scrubber over-matching a record address, and the table
//	renderer's own behaviour. Split out of memory_test.go for the
//	300-line file cap.
//
// SPORT: cmd.cascade.cmd.memory (ADD, per T-3 sport_updates).
package main

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestMemoryErrorsAreScrubbed is the redaction canary. It plants a machine
// path and a secret-shaped value in the error the daemon call returns and
// proves neither reaches the caller, while the taxonomy Kind — and so the
// process exit code — is preserved.
func TestMemoryErrorsAreScrubbed(t *testing.T) {
	const secret = "api_key=sk-live-9f3c2b7a1e4d8c6b5a0f2e9d"
	planted := cascade.Newf(cascade.KindUnavailable,
		"writing tombstone: open /Users/operator/.cascade/memory/project/x.md: %s", secret)
	h := &memoryHarness{err: planted}

	_, _, err := h.run(t, "forget", "project/x")
	if err == nil {
		t.Fatal("expected the planted error to surface")
	}
	text := err.Error()
	if strings.Contains(text, "/Users/operator") {
		t.Errorf("the operator's machine path reached the output: %q", text)
	}
	if strings.Contains(text, "sk-live-9f3c2b7a1e4d8c6b5a0f2e9d") {
		t.Errorf("a secret-shaped value reached the output: %q", text)
	}
	if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
		t.Errorf("kind = %v, want the original unavailable kind preserved", kind)
	}
	if !strings.Contains(text, "writing tombstone") {
		t.Errorf("scrubbing removed the diagnostic itself: %q", text)
	}
}

// TestScrubTextLeavesRecordAddressesAlone guards the scrubber's one
// dangerous failure mode: over-matching. A canonical address contains a
// slash, and mangling it would make every not-found message unactionable.
func TestScrubTextLeavesRecordAddressesAlone(t *testing.T) {
	cases := map[string]string{
		"no memory record project/a-note":    "no memory record project/a-note",
		"listing kind user: open /var/db/x":  "listing kind user: open [PATH-REDACTED]",
		`stat "C:\Users\op\.cascade\memory"`: `stat "[PATH-REDACTED]`,
		"cannot forget absent record user/x": "cannot forget absent record user/x",
	}
	for in, want := range cases {
		if got := scrubText(in); !strings.HasPrefix(got, want) {
			t.Errorf("scrubText(%q) = %q, want it to start with %q", in, got, want)
		}
	}
}

func TestUnitsViewRendersUnreadableRecords(t *testing.T) {
	view := unitsView{
		Unreadable: []memory.ProjectionFailure{{ID: "project/damaged", Reason: "malformed record at /home/op/x.md"}},
	}
	out := view.String()
	if !strings.Contains(out, "no records") || !strings.Contains(out, "project/damaged") {
		t.Errorf("output = %q, want an empty table plus the damaged record", out)
	}
	if strings.Contains(out, "/home/op") {
		t.Errorf("an unreadable record's reason leaked a machine path: %q", out)
	}
}

func TestSummarizeStripsControlCharactersAndTruncates(t *testing.T) {
	long := strings.Repeat("x", memorySummaryWidth+10)
	got := summarize(memory.MemoryEntry{Body: long})
	if len([]rune(got)) != memorySummaryWidth+3 || !strings.HasSuffix(got, "...") {
		t.Errorf("summarize truncation = %q", got)
	}
	escaped := summarize(memory.MemoryEntry{Description: "before\x1b[31mafter\tcell"})
	if strings.ContainsAny(escaped, "\x1b\t") {
		t.Errorf("summarize let a control character through: %q", escaped)
	}
}
