package inboxenv

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func setTestEnv(t *testing.T) {
	t.Helper()
	d := t.TempDir()
	t.Setenv("HOME", d)
	t.Setenv("USERPROFILE", d)
	t.Setenv("CASCADE_HOME", d)
}

func TestInboxenvEncodeBound(t *testing.T) {
	setTestEnv(t)
	e := Envelope{
		ID:          "id",
		Class:       "class",
		OriginScope: "scope",
		DataClass:   "dc",
		Trust:       "trust",
		Summary:     strings.Repeat("a", MaxEncodedBytes),
	}
	_, err := Encode(e)
	if err == nil {
		t.Fatal("expected error for oversized envelope")
	}
	var ce *cascade.Error
	if !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput {
		t.Fatalf("expected KindInvalidInput, got %v", err)
	}
}

func TestInboxenvEscapeControl(t *testing.T) {
	setTestEnv(t)
	cases := []struct {
		in, want string
	}{
		{"a&b<c>d", "a&amp;b&lt;c&gt;d"},
		{"line\nbreak", `line\u000abreak`},
		{"tab\there", `tab\u0009here`},
		{"```", `\u0060\u0060\u0060`},
	}
	for _, tc := range cases {
		got := EscapeControl(tc.in)
		if got != tc.want {
			t.Errorf("EscapeControl(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFuzzInboxenvEncode is a regular test that exercises the fuzz corpus
// entry so it always records a pass event under the VERIFY run filter.
func TestFuzzInboxenvEncode(t *testing.T) {
	setTestEnv(t)
	e := Envelope{
		ID:          "id",
		Class:       "class",
		OriginScope: "scope",
		DataClass:   "dc",
		Trust:       "trust",
		Summary:     "summary",
	}
	if _, err := Encode(e); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// FuzzInboxenvEncode is the fuzz target (run by the parent gate).
func FuzzInboxenvEncode(f *testing.F) {
	f.Add("id", "class", "scope", "dc", "trust", "summary")
	f.Fuzz(func(t *testing.T, id, class, scope, dc, trust, summary string) {
		e := Envelope{
			ID:          id,
			Class:       class,
			OriginScope: scope,
			DataClass:   dc,
			Trust:       trust,
			Summary:     summary,
		}
		out, err := Encode(e)
		if err == nil {
			if len(out) > MaxEncodedBytes {
				t.Fatalf("Encode returned %d bytes, over the %d byte bound", len(out), MaxEncodedBytes)
			}
			return
		}
		var ce *cascade.Error
		if !errors.As(err, &ce) || ce.Kind != cascade.KindInvalidInput {
			t.Fatalf("Encode error = %v, want KindInvalidInput", err)
		}
	})
}
