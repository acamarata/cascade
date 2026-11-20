package lessons

import (
	"errors"
	"testing"
)

type isNilHook struct {
	isNil bool
	calls int
}

func (h *isNilHook) Applied(caller string) {
	h.calls++
}

func (h *isNilHook) IsNil() bool {
	return h.isNil
}

func TestNewWithIsNilHookTrue(t *testing.T) {
	h := &isNilHook{isNil: true}
	a := New(nil, h)
	if a.hook != nil {
		t.Error("expected hook to be nil when IsNil() returns true")
	}
}

func TestNewWithIsNilHookFalse(t *testing.T) {
	h := &isNilHook{isNil: false}
	a := New(nil, h)
	if a.hook == nil {
		t.Error("expected hook to be kept when IsNil() returns false")
	}
	_, _ = a.Ask(Request{})
	if h.calls != 1 {
		t.Errorf("expected hook to be called once, got %d", h.calls)
	}
}

func TestApplyValidCallers(t *testing.T) {
	callers := []string{"ask", "loop", "submit", "swarm", "crunch", "forge"}
	for _, c := range callers {
		t.Run(c, func(t *testing.T) {
			h := &isNilHook{isNil: false}
			a := New([]Lesson{func(r Request) Request { r.Text += c; return r }}, h)
			res, err := a.Apply(c, Request{Text: "start-"})
			if err != nil {
				t.Fatalf("Apply(%q) unexpected error: %v", c, err)
			}
			if res.Text != "start-"+c {
				t.Errorf("Apply(%q) text = %q, want %q", c, res.Text, "start-"+c)
			}
			if h.calls != 1 {
				t.Errorf("Apply(%q) hook calls = %d, want 1", c, h.calls)
			}
		})
	}
}

func TestApplyInvalidLesson(t *testing.T) {
	a := New([]Lesson{nil}, nil)
	_, err := a.Apply("ask", Request{Text: "test"})
	if !errors.Is(err, ErrInvalidLesson) {
		t.Errorf("Apply with invalid lesson error = %v, want %v", err, ErrInvalidLesson)
	}
}
