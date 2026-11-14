package lifecycle

import (
	"strings"
	"testing"
)

func TestEveryTemplateRendersItsSample(t *testing.T) {
	tests := []struct {
		kind   TemplateKind
		name   string
		data   TemplateData
		checks []string
	}{
		{
			kind: KindSeeded,
			name: "seeded",
			data: TemplateData{
				ProjectID: "p42",
			},
			checks: []string{"p42", "created", "plan"},
		},
		{
			kind: KindCreated,
			name: "created",
			data: TemplateData{
				ProjectID: "p99",
			},
			checks: []string{"p99", "created"},
		},
		{
			kind: KindTicket,
			name: "ticket",
			data: TemplateData{
				TicketID: "t1",
				Progress: []string{"started work", "fixed bug"},
			},
			checks: []string{"t1", "started work", "fixed bug"},
		},
		{
			kind: KindCrunch,
			name: "crunch",
			data: TemplateData{
				ProjectID: "p7",
			},
			checks: []string{"p7", "update"},
		},
	}

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
