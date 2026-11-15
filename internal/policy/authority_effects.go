// Package policy (authority_effects.go): Purpose: define effect classes
// for authority-boundary policy checks.
//
// Inputs: string representations of effect classes.
// Outputs: typed EffectClass values or a closed refusal error.
//
// Constraints: the set of effect classes is closed; any unknown string
// produces an ErrUnknownEffectClass refusal.
package policy

import (
	"errors"
	"fmt"
)

// EffectClass represents a closed set of effect categories for
// authority-boundary policy decisions.
type EffectClass string

// The closed set of EffectClass constants.
const (
	// EffectCredential marks effects touching credential material.
	EffectCredential EffectClass = "credential"
	// EffectSpend marks effects that commit a financial spend.
	EffectSpend EffectClass = "spend"
	// EffectLegalTerms marks effects that bind legal terms.
	EffectLegalTerms EffectClass = "legal_terms"
	// EffectIrreversibleDestructive marks effects that destroy data
	// without a rollback path.
	EffectIrreversibleDestructive EffectClass = "irreversible_destructive"
	// EffectExternalOutbound marks effects that send data outside the
	// trust boundary.
	EffectExternalOutbound EffectClass = "external_outbound"
	// EffectPublicIdentity marks effects that expose a public identity.
	EffectPublicIdentity EffectClass = "public_identity"
)

// ErrUnknownEffectClass is the closed refusal returned by ParseEffectClass
// when the input string does not match any known effect class.
var ErrUnknownEffectClass = errors.New("unknown_effect_class")

// ParseEffectClass converts a string into an EffectClass. It returns
// ErrUnknownEffectClass for any unrecognized input.
func ParseEffectClass(s string) (EffectClass, error) {
	switch s {
	case string(EffectCredential):
		return EffectCredential, nil
	case string(EffectSpend):
		return EffectSpend, nil
	case string(EffectLegalTerms):
		return EffectLegalTerms, nil
	case string(EffectIrreversibleDestructive):
		return EffectIrreversibleDestructive, nil
	case string(EffectExternalOutbound):
		return EffectExternalOutbound, nil
	case string(EffectPublicIdentity):
		return EffectPublicIdentity, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownEffectClass, s)
	}
}
