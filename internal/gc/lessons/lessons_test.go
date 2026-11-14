package lessons

import (
	"errors"
	"testing"
)

// TestSameResultAcrossCallers verifies that ask and forge produce identical
// results for the same request, and that defensive copying prevents external
// mutation from affecting the pipeline.
func TestSameResultAcrossCallers(t *testing.T) {
	appendA := func(r Request) Request { return Request{Text: r.Text + "A"} }
	appendB := func(r Request) Request { return Request{Text: r.Text + "B"} }
	lessons := []Lesson{appendA, appendB}

	applier := New(lessons, nil)
	if applier == nil {
		t.Fatal("New returned nil")
	}

	input := Request{Text: "start"}
	expected := Request{Text: "startAB"}

	askResult, err := applier.Ask(input)
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}
	if askResult != expected {
		t.Errorf("Expected %v, got %v", expected, askResult)
	}

	forgeResult, err := applier.Forge(input)
	if err != nil {
		t.Fatalf("Forge returned error: %v", err)
	}
	if askResult != forgeResult {
		t.Errorf("Ask and Forge produced different results: ask=%v, forge=%v", askResult, forgeResult)
	}

	verifyDefensiveCopying(t, applier, lessons, input, expected)
}

func verifyDefensiveCopying(t *testing.T, applier *Applier, lessons []Lesson, input Request, expected Request) {
	// Verify defensive copying: mutate the original slice
	lessons[0] = func(r Request) Request {
		return Request{Text: r.Text + "X"}
	}

	// The applier should still use the original lessons, not the mutated ones
	askResult2, err := applier.Ask(input)
	if err != nil {
		t.Fatalf("Ask returned error after mutation: %v", err)
	}
	if askResult2 != expected {
		t.Errorf("Applier was affected by external slice mutation: expected %v, got %v", expected, askResult2)
	}

	forgeResult2, err := applier.Forge(input)
	if err != nil {
		t.Fatalf("Forge returned error after mutation: %v", err)
	}
	if forgeResult2 != expected {
		t.Errorf("Applier was affected by external slice mutation: expected %v, got %v", expected, forgeResult2)
	}
}

// TestNilLessonMarksInvalid verifies that a nil lesson in the slice marks
// the applier as invalid and returns ErrInvalidLesson.
func TestNilLessonMarksInvalid(t *testing.T) {
	lessons := []Lesson{nil, func(r Request) Request { return r }}
	applier := New(lessons, nil)

	_, err := applier.Ask(Request{Text: "test"})
	if !errors.Is(err, ErrInvalidLesson) {
		t.Errorf("Expected ErrInvalidLesson, got %v", err)
	}
}

// TestNilApplierReturnsError verifies that a nil Applier returns ErrInvalidApplier.
func TestNilApplierReturnsError(t *testing.T) {
	var applier *Applier
	_, err := applier.Ask(Request{Text: "test"})
	if !errors.Is(err, ErrInvalidApplier) {
		t.Errorf("Expected ErrInvalidApplier, got %v", err)
	}
}

// TestHookIsCalled verifies that the hook is called with the correct caller name.
func TestHookIsCalled(t *testing.T) {
	var calledWith string
	hook := &testHook{
		applied: func(caller string) {
			calledWith = caller
		},
	}

	lessons := []Lesson{func(r Request) Request { return r }}
	applier := New(lessons, hook)

	_, err := applier.Ask(Request{Text: "test"})
	if err != nil {
		t.Fatalf("Ask returned error: %v", err)
	}

	if calledWith != "ask" {
		t.Errorf("Hook called with %q, expected %q", calledWith, "ask")
	}

	_, err = applier.Forge(Request{Text: "test"})
	if err != nil {
		t.Fatalf("Forge returned error: %v", err)
	}

	if calledWith != "forge" {
		t.Errorf("Hook called with %q, expected %q", calledWith, "forge")
	}
}

type testHook struct {
	applied func(string)
}

func (h *testHook) Applied(caller string) {
	if h.applied != nil {
		h.applied(caller)
	}
}
