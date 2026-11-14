package lifecycle

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// templateSample is one template kind with sample data and substrings its rendering must contain.
type templateSample struct {
	kind   TemplateKind
	name   string
	data   TemplateData
	checks []string
}

// templateSamples lists the samples TestEveryTemplateRendersItsSample renders.
func templateSamples() []templateSample {
	return []templateSample{
		{kind: KindSeeded, name: "seeded", data: TemplateData{ProjectID: "p42"}, checks: []string{"p42", "created", "plan"}},
		{kind: KindCreated, name: "created", data: TemplateData{ProjectID: "p99"}, checks: []string{"p99", "created"}},
		{kind: KindTicket, name: "ticket", data: TemplateData{TicketID: "t1", Progress: []string{"started work", "fixed bug"}},
			checks: []string{"t1", "started work", "fixed bug"}},
		{kind: KindCrunch, name: "crunch", data: TemplateData{ProjectID: "p7"}, checks: []string{"p7", "update"}},
	}
}

func TestEveryTemplateRendersItsSample(t *testing.T) {
	tests := templateSamples()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Render(tt.kind, tt.data)
			if err != nil {
				t.Fatalf("Render(%s) error: %v", tt.kind, err)
			}
			for _, check := range tt.checks {
				if !strings.Contains(result, check) {
					t.Errorf("Render(%s) = %q, want substring %q", tt.kind, result, check)
				}
			}
			if strings.Contains(result, "—") {
				t.Errorf("Render(%s) = %q, contains em-dash", tt.kind, result)
			}
			if strings.Contains(result, "actually") || strings.Contains(result, "basically") || strings.Contains(result, "just") {
				t.Errorf("Render(%s) = %q, contains filler words", tt.kind, result)
			}
		})
	}
}

func TestPlanConfirmEndsWithAskCard(t *testing.T) {
	askJSON := `{"kind":"decision","question":"Ready to forge?"}`
	data := TemplateData{
		ProjectID: "p42",
		AskBlock:  askJSON,
	}
	output, err := Render(KindPlanConfirm, data)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	trimmed := strings.TrimRight(output, "\n")
	expectedEnd := "```ask\n" + askJSON + "\n```"
	if !strings.HasSuffix(trimmed, expectedEnd) {
		t.Errorf("Output does not end with expected ask card.\nGot: %q\nExpected suffix: %q", trimmed, expectedEnd)
	}
	idx := strings.LastIndex(trimmed, "```ask\n")
	body := trimmed[idx+len("```ask\n"):]
	body = strings.TrimSuffix(body, "\n```")
	if !json.Valid([]byte(body)) {
		t.Errorf("Ask block body is not valid JSON: %q", body)
	}
}

func TestCompleteShowsReportAndAutoSeed(t *testing.T) {
	data := TemplateData{
		ProjectID: "p42",
		Progress:  []string{"Landed 40, parked 2"},
		Decisions: []string{"a", "b"},
	}
	output, err := Render(KindComplete, data)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if !strings.Contains(output, "Landed 40, parked 2") {
		t.Errorf("Missing progress")
	}
	if !strings.Contains(output, "1. a") {
		t.Errorf("Missing decision 1")
	}
	if !strings.Contains(output, "2. b") {
		t.Errorf("Missing decision 2")
	}
	if !strings.Contains(output, "Next: the following phase is seeded automatically.") {
		t.Errorf("Missing auto-seed message")
	}
	matched, _ := regexp.MatchString(`(?i)\byes\b`, output)
	if matched {
		t.Errorf("Output contains 'yes': %q", output)
	}
}

func TestCompleteAskCardOnlyForOwnerItems(t *testing.T) {
	data1 := TemplateData{
		ProjectID: "p42",
		Progress:  []string{"Landed 40"},
		Decisions: []string{"a"},
		AskBlock:  "",
	}
	out1, err := Render(KindComplete, data1)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if strings.Contains(out1, "```ask") {
		t.Errorf("Empty AskBlock should not produce an ask card, got: %q", out1)
	}

	askJSON := `{"kind":"decision","question":"Ready?"}`
	data2 := TemplateData{
		ProjectID: "p42",
		Progress:  []string{"Landed 40"},
		Decisions: []string{"a"},
		AskBlock:  askJSON,
	}
	out2, err := Render(KindComplete, data2)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	trimmed := strings.TrimRight(out2, "\n")
	if !strings.HasSuffix(trimmed, "```ask\n"+askJSON+"\n```") {
		t.Errorf("Ask card is not the last block, got: %q", trimmed)
	}
}
