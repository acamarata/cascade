package decide

import (
	"errors"
	"testing"
)

func TestDecideEmptyInputFailsClosed(t *testing.T) {
	called := false
	fakeRule := func(_ Question) (string, bool) {
		called = true
		return YES, true
	}

	cases := []struct {
		name string
		q    Question
	}{
		{"empty kind", Question{Kind: "", Text: "hello", Rules: []Rule{fakeRule}}},
		{"whitespace kind", Question{Kind: "   ", Text: "hello", Rules: []Rule{fakeRule}}},
		{"empty text", Question{Kind: "k", Text: "", Rules: []Rule{fakeRule}}},
		{"whitespace text", Question{Kind: "k", Text: "\t\n", Rules: []Rule{fakeRule}}},
		{"nul in kind", Question{Kind: "a\x00b", Text: "hello", Rules: []Rule{fakeRule}}},
		{"nul in text", Question{Kind: "k", Text: "hel\x00lo", Rules: []Rule{fakeRule}}},
		{"nil rule entry", Question{Kind: "k", Text: "hello", Rules: []Rule{fakeRule, nil}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			v, err := Decide(tc.q)
			if v != (Verdict{}) {
				t.Errorf("expected zero Verdict, got %+v", v)
			}
			if !errors.Is(err, ErrBadQuestion) {
				t.Errorf("expected ErrBadQuestion, got %v", err)
			}
			if called {
				t.Error("rule was called on invalid input")
			}
		})
	}
}

// matched invalid answer returns the same error and no later calls
func TestDecideMatchedInvalidAnswerFailsClosed(t *testing.T) {
	callCount := 0
	badRule := func(_ Question) (string, bool) {
		callCount++
		return "INVALID_ANSWER", true
	}
	afterRule := func(_ Question) (string, bool) {
		callCount++
		return YES, true
	}
	q := Question{Kind: "k", Text: "t", Rules: []Rule{badRule, afterRule}}
	v, err := Decide(q)
	if v != (Verdict{}) {
		t.Errorf("expected zero Verdict, got %+v", v)
	}
	if !errors.Is(err, ErrBadQuestion) {
		t.Errorf("expected ErrBadQuestion, got %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected exactly 1 rule call, got %d", callCount)
	}
}

// ErrBadQuestion message
func TestDecideErrorMessage(t *testing.T) {
	if ErrBadQuestion.Error() != "bad question" {
		t.Errorf("unexpected error message: %s", ErrBadQuestion.Error())
	}
}
