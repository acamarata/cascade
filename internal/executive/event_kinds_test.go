package executive

import (
	"errors"
	"sort"
	"testing"
)

// TestEXE01LegacyAC1 verifies the closed exec.* kind set.
func TestEXE01LegacyAC1(t *testing.T) {
	expected := []Kind{
		KindTaskStarted,
		KindTaskCompleted,
		KindTaskFailed,
		KindRunStarted,
		KindRunCompleted,
		KindRunFailed,
	}

	if len(AllKinds) != len(expected) {
		t.Fatalf("expected %d kinds, got %d", len(expected), len(AllKinds))
	}

	got := make([]Kind, len(AllKinds))
	copy(got, AllKinds)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	exp := make([]Kind, len(expected))
	copy(exp, expected)
	sort.Slice(exp, func(i, j int) bool { return exp[i] < exp[j] })

	for i := range exp {
		if got[i] != exp[i] {
			t.Fatalf("kind %d: expected %q, got %q", i, exp[i], got[i])
		}
	}
}

// TestEXE01LegacyAC2 verifies ParseExecKind returns ErrUnknownExecKind for unset or unknown kinds.
func TestEXE01LegacyAC2(t *testing.T) {
	cases := []struct {
		input string
		want  error
	}{
		{"", ErrUnknownExecKind},
		{"exec.unknown", ErrUnknownExecKind},
		{"invalid", ErrUnknownExecKind},
	}

	for _, tc := range cases {
		_, err := ParseExecKind(tc.input)
		if !errors.Is(err, tc.want) {
			t.Errorf("ParseExecKind(%q) error = %v, want %v", tc.input, err, tc.want)
		}
	}
}
