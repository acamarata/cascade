package governor

// Purpose: asserts every escalation sentinel is a real pkg/cascade
// taxonomy error carrying the expected Kind, and that EscalationExhausted
// (resource exhaustion) is distinguishable from ErrEscalationRungFailed
// and ErrEscalationJournalUnavailable via cascade.KindOf.

import (
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestEscalationErrorSentinelsAreTaxonomyKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want cascade.Kind
	}{
		{"EscalationExhausted", EscalationExhausted, cascade.KindQuotaExhausted},
		{"ErrEscalationInvalidInput", ErrEscalationInvalidInput, cascade.KindInvalidInput},
		{"ErrEscalationJournalUnavailable", ErrEscalationJournalUnavailable, cascade.KindUnavailable},
		{"ErrEscalationRungFailed", ErrEscalationRungFailed, cascade.KindUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, ok := cascade.KindOf(tc.err)
			if !ok {
				t.Fatalf("%s is not a taxonomy error", tc.name)
			}
			if kind != tc.want {
				t.Fatalf("%s kind = %s, want %s", tc.name, kind, tc.want)
			}
		})
	}
}

// TestEscalationExhaustionIsDistinguishable proves a caller can tell "the
// ladder is fully spent, a human must act" (EscalationExhausted) apart
// from "this one rung failed but the ladder moved on"
// (ErrEscalationRungFailed): different Kinds, so errors.Is never conflates
// them.
func TestEscalationExhaustionIsDistinguishable(t *testing.T) {
	if errors.Is(EscalationExhausted, ErrEscalationRungFailed) {
		t.Fatal("EscalationExhausted must not be Is-equivalent to ErrEscalationRungFailed")
	}
	if !errors.Is(EscalationExhausted, EscalationExhausted) {
		t.Fatal("EscalationExhausted must be Is-equivalent to itself")
	}
}
