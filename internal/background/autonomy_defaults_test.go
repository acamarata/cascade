package background

import (
	"errors"
	"testing"
)

func TestRecordedIntentDecides(t *testing.T) {
	intents := map[string]string{
		"tabs or spaces": "tabs",
		"  ":             "blank key",
		"blank val":      "   ",
	}

	tests := []struct {
		name     string
		mode     string
		question string
		def      string
		wantAns  string
		wantErr  error
	}{
		{
			name:     "bypass mode returns recorded intent",
			mode:     ModeFullAutonomy,
			question: "tabs or spaces",
			def:      "spaces",
			wantAns:  "tabs",
			wantErr:  nil,
		},
		{
			name:     "normal mode returns recorded intent",
			mode:     "normal",
			question: "tabs or spaces",
			def:      "spaces",
			wantAns:  "tabs",
			wantErr:  nil,
		},
		{
			name:     "bypass mode uses sensible default when not recorded",
			mode:     ModeFullAutonomy,
			question: "new question",
			def:      "new default",
			wantAns:  "new default",
			wantErr:  nil,
		},
		{
			name:     "normal mode returns ErrNeedsUser when not recorded",
			mode:     "normal",
			question: "new question",
			def:      "new default",
			wantAns:  "",
			wantErr:  ErrNeedsUser,
		},
		{
			name:     "blank question returns ErrInvalidQuestion",
			mode:     ModeFullAutonomy,
			question: "",
			def:      "default",
			wantAns:  "",
			wantErr:  ErrInvalidQuestion,
		},
		{
			name:     "whitespace question returns ErrInvalidQuestion",
			mode:     ModeFullAutonomy,
			question: "   ",
			def:      "default",
			wantAns:  "",
			wantErr:  ErrInvalidQuestion,
		},
		{
			name:     "question with surrounding whitespace returns ErrInvalidQuestion",
			mode:     ModeFullAutonomy,
			question: " spaced ",
			def:      "default",
			wantAns:  "",
			wantErr:  ErrInvalidQuestion,
		},
		{
			name:     "bypass mode with blank default returns ErrInvalidQuestion",
			mode:     ModeFullAutonomy,
			question: "unrecorded",
			def:      "   ",
			wantAns:  "",
			wantErr:  ErrInvalidQuestion,
		},
	}

	for _, tt := range tests {
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

	t.Run("constructor copies input map", func(t *testing.T) {
		r := NewResolver(ModeFullAutonomy, intents)
		intents["tabs or spaces"] = "mutated"
		ans, err := r.ResolveQuestion("tabs or spaces", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ans != "tabs" {
			t.Errorf("expected 'tabs', got %q", ans)
		}
	})

	t.Run("constructor ignores blank entries", func(t *testing.T) {
		r := NewResolver(ModeFullAutonomy, intents)
		ans, err := r.ResolveQuestion("blank val", "default val")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ans != "default val" {
			t.Errorf("expected 'default val' because blank val was ignored, got %q", ans)
		}
	})

	t.Run("nil receiver returns ErrInvalidQuestion", func(t *testing.T) {
		var r *Resolver
		_, err := r.ResolveQuestion("q", "d")
		if !errors.Is(err, ErrInvalidQuestion) {
			t.Errorf("expected ErrInvalidQuestion, got %v", err)
		}
	})
}
