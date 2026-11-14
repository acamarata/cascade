package background

import (
	"errors"
	"testing"
)

func recordedIntents() map[string]string {
	return map[string]string{
		"tabs or spaces": "tabs",
		"  ":             "blank key",
		"blank val":      "   ",
	}
}

type recordedIntentCase struct {
	name     string
	mode     string
	question string
	def      string
	wantAns  string
	wantErr  error
}

func recordedIntentCases() []recordedIntentCase {
	return []recordedIntentCase{
		{"bypass mode returns recorded intent", ModeFullAutonomy, "tabs or spaces", "spaces", "tabs", nil},
		{"normal mode returns recorded intent", "normal", "tabs or spaces", "spaces", "tabs", nil},
		{"bypass mode uses sensible default when not recorded", ModeFullAutonomy, "new question", "new default", "new default", nil},
		{"normal mode returns ErrNeedsUser when not recorded", "normal", "new question", "new default", "", ErrNeedsUser},
		{"blank question returns ErrInvalidQuestion", ModeFullAutonomy, "", "default", "", ErrInvalidQuestion},
		{"whitespace question returns ErrInvalidQuestion", ModeFullAutonomy, "   ", "default", "", ErrInvalidQuestion},
		{"question with surrounding whitespace returns ErrInvalidQuestion", ModeFullAutonomy, " spaced ", "default", "", ErrInvalidQuestion},
		{"bypass mode with blank default returns ErrInvalidQuestion", ModeFullAutonomy, "unrecorded", "   ", "", ErrInvalidQuestion},
	}
}

func TestRecordedIntentDecides(t *testing.T) {
	intents := recordedIntents()

	for _, tt := range recordedIntentCases() {
		t.Run(tt.name, func(t *testing.T) {
			r := NewResolver(tt.mode, intents)
			ans, err := r.ResolveQuestion(tt.question, tt.def)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("ResolveQuestion() error = %v, wantErr %v", err, tt.wantErr)
			}
			if ans != tt.wantAns {
				t.Errorf("ResolveQuestion() ans = %q, want %q", ans, tt.wantAns)
			}
		})
	}
}

func TestResolverConstructorCopiesInputMap(t *testing.T) {
	intents := recordedIntents()
	r := NewResolver(ModeFullAutonomy, intents)
	intents["tabs or spaces"] = "mutated"
	ans, err := r.ResolveQuestion("tabs or spaces", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans != "tabs" {
		t.Errorf("expected 'tabs', got %q", ans)
	}
}

func TestResolverConstructorIgnoresBlankEntries(t *testing.T) {
	r := NewResolver(ModeFullAutonomy, recordedIntents())
	ans, err := r.ResolveQuestion("blank val", "default val")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ans != "default val" {
		t.Errorf("expected 'default val' because blank val was ignored, got %q", ans)
	}
}

func TestResolverNilReceiverReturnsErrInvalidQuestion(t *testing.T) {
	var r *Resolver
	_, err := r.ResolveQuestion("q", "d")
	if !errors.Is(err, ErrInvalidQuestion) {
		t.Errorf("expected ErrInvalidQuestion, got %v", err)
	}
}
