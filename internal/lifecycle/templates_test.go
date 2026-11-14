package lifecycle

import (
	"regexp"
	"strings"
	"testing"
)

// TestRenderUnknownKindReturnsError verifies that Render returns an error
// when called with a kind that has not been registered.
func TestRenderUnknownKindReturnsError(t *testing.T) {
	_, err := Render("unknown_kind", TemplateData{})
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
}

// TestNoEmDashesOrFillerWords renders each registered template with sample data
// and asserts no em-dash characters and no filler words appear in output.
func TestNoEmDashesOrFillerWords(t *testing.T) {
	kinds := []TemplateKind{
		KindSeeded, KindCreated, KindTicket, KindCrunch,
		KindPlanConfirm, KindComplete,
	}
	data := TemplateData{
		ProjectID: "proj-1",
		TicketID:  "ticket-1",
		AskBlock:  "Do you want to proceed?",
		Decisions: []string{"Approve plan"},
		Progress:  []string{"Step 1"},
	}

	for _, k := range kinds {
		out, err := Render(k, data)
		if err != nil {
			t.Fatalf("render %s: %v", k, err)
		}
		if strings.Contains(out, "—") {
			t.Errorf("%s contains em-dash", k)
		}
		for _, filler := range []string{"basically", "actually", "just"} {
			if strings.Contains(strings.ToLower(out), filler) {
				t.Errorf("%s contains filler word %q", k, filler)
			}
		}
	}
}

// TestAskCardsParse verifies that the trailing ask card substring is parseable
// by the existing parser pattern for KindPlanConfirm and KindComplete.
func TestAskCardsParse(t *testing.T) {
	kinds := []TemplateKind{KindPlanConfirm, KindComplete}
	data := TemplateData{
		ProjectID: "proj-1",
		TicketID:  "ticket-1",
		AskBlock:  "Do you want to proceed with this plan?",
		Decisions: []string{"Approve plan"},
		Progress:  []string{"Step 1"},
	}

	askPattern := regexp.MustCompile("(?s)```ask\n.*?```")

	for _, k := range kinds {
		out, err := Render(k, data)
		if err != nil {
			t.Fatalf("render %s: %v", k, err)
		}
		if !askPattern.MatchString(out) {
			t.Errorf("%s does not contain a parseable trailing ask card: %q", k, out)
		}
	}
}

// TestCompleteListsDecisionsMadeForYou renders KindComplete with multiple
// decisions and asserts each appears in the output.
func TestCompleteListsDecisionsMadeForYou(t *testing.T) {
	data := TemplateData{
		ProjectID: "proj-1",
		TicketID:  "ticket-1",
		Decisions: []string{"Approve plan", "Reject plan", "Change scope"},
		Progress:  []string{"Step 1"},
	}

	out, err := Render(KindComplete, data)
	if err != nil {
		t.Fatalf("render %s: %v", KindComplete, err)
	}

	for _, d := range data.Decisions {
		if !strings.Contains(out, d) {
			t.Errorf("KindComplete output missing decision %q", d)
		}
	}
}
