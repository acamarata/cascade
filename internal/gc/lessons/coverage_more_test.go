package lessons

import (
	"errors"
	"testing"
)

// nilAwareHook implements Hook plus IsNil so New can detect typed-nil hooks.
type nilAwareHook struct {
	calls []string
	isNil bool
}

func (h *nilAwareHook) Applied(caller string) {
	h.calls = append(h.calls, caller)
}

func (h *nilAwareHook) IsNil() bool { return h.isNil }

func suffix(s string) Lesson {
	return func(r Request) Request { return Request{Text: r.Text + s} }
}

func TestApplySuccessAllCallers(t *testing.T) {
	callers := []string{"ask", "loop", "submit", "swarm", "crunch", "forge"}
	for _, c := range callers {
		t.Run(c, func(t *testing.T) {
			h := &countingHook{}
			a := New([]Lesson{suffix("A"), suffix("B"), suffix("A")}, h)
			got, err := a.Apply(c, Request{Text: "x"})
			if err != nil {
				t.Fatalf("Apply(%q) error = %v", c, err)
			}
			if got.Text != "xABA" {
				t.Errorf("Apply(%q) = %q, want %q", c, got.Text, "xABA")
			}
			if h.calls != 1 {
				t.Errorf("hook calls = %d, want 1", h.calls)
			}
		})
	}
}

func TestApplyHookReceivesCaller(t *testing.T) {
	h := &nilAwareHook{}
	a := New(nil, h)
	if _, err := a.Apply("swarm", Request{Text: "q"}); err != nil {
		t.Fatalf("Apply error = %v", err)
	}
	if len(h.calls) != 1 || h.calls[0] != "swarm" {
		t.Errorf("hook calls = %v, want [swarm]", h.calls)
	}
}

func TestApplyInvalidLesson(t *testing.T) {
	h := &countingHook{}
	ran := false
	a := New([]Lesson{func(r Request) Request { ran = true; return r }, nil}, h)
	got, err := a.Apply("loop", Request{Text: "x"})
	if !errors.Is(err, ErrInvalidLesson) {
		t.Fatalf("error = %v, want ErrInvalidLesson", err)
	}
	if got != (Request{}) {
		t.Errorf("got %v, want zero Request", got)
	}
	if ran || h.calls != 0 {
		t.Errorf("lesson ran=%v hook calls=%d, want neither", ran, h.calls)
	}
}

func TestApplyUnknownBeforeInvalid(t *testing.T) {
	a := New([]Lesson{nil}, nil)
	_, err := a.Apply("nope", Request{})
	if !errors.Is(err, ErrUnknownCaller) {
		t.Errorf("error = %v, want ErrUnknownCaller", err)
	}
}

func TestApplyNilHookAndEmptyPipeline(t *testing.T) {
	tests := []struct {
		name    string
		lessons []Lesson
		want    string
	}{
		{"nil slice", nil, "in"},
		{"empty slice", []Lesson{}, "in"},
		{"one lesson", []Lesson{suffix("!")}, "in!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(tt.lessons, nil)
			got, err := a.Apply("ask", Request{Text: "in"})
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if got.Text != tt.want {
				t.Errorf("got %q, want %q", got.Text, tt.want)
			}
		})
	}
}

func TestNewHookNilDetection(t *testing.T) {
	tests := []struct {
		name     string
		hook     Hook
		wantHook bool
	}{
		{"nil interface", nil, false},
		{"IsNil true", &nilAwareHook{isNil: true}, false},
		{"IsNil false", &nilAwareHook{isNil: false}, true},
		{"plain hook", &countingHook{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(nil, tt.hook)
			if (a.hook != nil) != tt.wantHook {
				t.Errorf("hook set = %v, want %v", a.hook != nil, tt.wantHook)
			}
			if _, err := a.Apply("ask", Request{}); err != nil {
				t.Errorf("Apply error = %v", err)
			}
		})
	}
}

func TestNewIsNilHookNotCalled(t *testing.T) {
	h := &nilAwareHook{isNil: true}
	a := New([]Lesson{suffix("z")}, h)
	got, err := a.Ask(Request{Text: "a"})
	if err != nil || got.Text != "az" {
		t.Fatalf("Ask = %v, %v; want az, nil", got, err)
	}
	if len(h.calls) != 0 {
		t.Errorf("nil-reporting hook was called: %v", h.calls)
	}
}

func TestNewCopiesAndFlagsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		lessons []Lesson
		invalid bool
	}{
		{"none", nil, false},
		{"valid", []Lesson{suffix("a")}, false},
		{"nil first", []Lesson{nil}, true},
		{"nil last", []Lesson{suffix("a"), nil}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := New(tt.lessons, nil)
			if a.invalid != tt.invalid {
				t.Errorf("invalid = %v, want %v", a.invalid, tt.invalid)
			}
			if len(a.lessons) != len(tt.lessons) {
				t.Errorf("len = %d, want %d", len(a.lessons), len(tt.lessons))
			}
		})
	}
}

func TestAskForgeErrorPaths(t *testing.T) {
	var nilA *Applier
	bad := New([]Lesson{nil}, nil)
	tests := []struct {
		name string
		call func(Request) (Request, error)
		want error
	}{
		{"ask nil", nilA.Ask, ErrInvalidApplier},
		{"forge nil", nilA.Forge, ErrInvalidApplier},
		{"ask invalid", bad.Ask, ErrInvalidLesson},
		{"forge invalid", bad.Forge, ErrInvalidLesson},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call(Request{Text: "keep"})
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if got.Text != "keep" {
				t.Errorf("request not returned unchanged: %q", got.Text)
			}
		})
	}
}

func TestSentinelsDistinct(t *testing.T) {
	all := []error{ErrUnknownCaller, ErrInvalidLesson, ErrInvalidApplier}
	for i, s := range all {
		if s.Error() == "" {
			t.Errorf("sentinel %d has no text", i)
		}
		for j, o := range all {
			if i != j && errors.Is(s, o) {
				t.Errorf("%q matches %q", s, o)
			}
		}
	}
}
