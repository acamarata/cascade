package lessons

import "testing"

type countingHook struct {
	calls int
}

func (h *countingHook) Applied(caller string) {
	h.calls++
}

func TestUnknownCallerRejected(t *testing.T) {
	called := false
	lesson := func(r Request) Request {
		called = true
		return r
	}
	hook := &countingHook{}
	a := New([]Lesson{lesson}, hook)

	tests := []struct {
		name   string
		caller string
	}{
		{"empty", ""},
		{"uppercase", "ASK"},
		{"bogus", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			hook.calls = 0
			got, err := a.Apply(tt.caller, Request{Text: "hello"})
			if err != ErrUnknownCaller {
				t.Errorf("Apply(%q) error = %v, want %v", tt.caller, err, ErrUnknownCaller)
			}
			if got != (Request{}) {
				t.Errorf("Apply(%q) returned non-zero Request: %v", tt.caller, got)
			}
			if called {
				t.Errorf("Apply(%q) called lesson", tt.caller)
			}
			if hook.calls != 0 {
				t.Errorf("Apply(%q) called hook", tt.caller)
			}
		})
	}

	t.Run("nil applier", func(t *testing.T) {
		var nilA *Applier
		got, err := nilA.Apply("ask", Request{Text: "hello"})
		if err != ErrInvalidApplier {
			t.Errorf("Apply on nil Applier error = %v, want %v", err, ErrInvalidApplier)
		}
		if got != (Request{}) {
			t.Errorf("Apply on nil Applier returned non-zero Request: %v", got)
		}
	})
}
