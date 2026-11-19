package background

import (
	"errors"
	"testing"
)

func TestUnresolvedTakesDefault(t *testing.T) {
	var nilR *Resolver
	if _, err := nilR.ResolveQuestion("q", "d"); !errors.Is(err, ErrInvalidQuestion) {
		t.Errorf("nil receiver err = %v, want %v", err, ErrInvalidQuestion)
	}

	r := NewResolver(ModeFullAutonomy, nil)

	cases := []struct {
		name     string
		question string
		def      string
		want     string
		err      error
		records  bool
	}{
		{"unresolved takes default", "log level", "info", "info", nil, true},
		{"repeated returns stored", "log level", "debug", "info", nil, false},
		{"blank question", "", "info", "", ErrInvalidQuestion, false},
		{"padded question", " log ", "info", "", ErrInvalidQuestion, false},
		{"blank default", "new key", "", "", ErrInvalidQuestion, false},
		{"whitespace only default", "new key 2", "   ", "", ErrInvalidQuestion, false},
		{"padded default", "new key 3", " info ", "", ErrInvalidQuestion, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prevLen := len(r.defaults)
			got, err := r.ResolveQuestion(c.question, c.def)
			if !errors.Is(err, c.err) {
				t.Errorf("err = %v, want %v", err, c.err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			newLen := len(r.defaults)
			if c.records && newLen == prevLen {
				t.Errorf("expected new record, but none added")
			}
			if !c.records && newLen > prevLen {
				t.Errorf("unexpected new record added")
			}
		})
	}
}
