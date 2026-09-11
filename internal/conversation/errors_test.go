package conversation

// Purpose: errors.go's own tests -- each sentinel carries the Kind its
//   doc comment claims, and no sentinel's static message text contains
//   any of this package's own field/type names as a smuggled format
//   placeholder (a canary against someone later "helpfully" adding
//   %v/%s interpolation of a Turn/Segment into one of these strings).
// SPORT: internal.conversation.errors/ADDED (P1-E20-W5-S43-T1).

import (
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSentinelKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want cascade.Kind
	}{
		{"ErrImmutable", ErrImmutable, cascade.KindConflict},
		{"ErrOutOfOrder", ErrOutOfOrder, cascade.KindInvalidInput},
		{"ErrTurnNotFound", ErrTurnNotFound, cascade.KindNotFound},
		{"ErrInvalidRecord", ErrInvalidRecord, cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		ce, ok := tc.err.(*cascade.Error)
		if !ok {
			t.Fatalf("%s is not a *cascade.Error", tc.name)
		}
		if ce.Kind != tc.want {
			t.Errorf("%s.Kind = %v, want %v", tc.name, ce.Kind, tc.want)
		}
	}
}

// TestSentinelMessagesAreStaticLiterals guards against a future edit
// turning one of these into a Newf call that formats caller content in:
// every message here must be a fixed string containing no printf verb.
func TestSentinelMessagesAreStaticLiterals(t *testing.T) {
	for _, err := range []error{ErrImmutable, ErrOutOfOrder, ErrTurnNotFound, ErrInvalidRecord} {
		msg := err.Error()
		if strings.ContainsAny(msg, "%") {
			t.Errorf("sentinel message %q contains a printf verb; sentinels must be static literals", msg)
		}
	}
}
